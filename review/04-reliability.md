# Marina Reliability Review

**Reviewer**: `reliability-master`
**Brief**: error handling, partial-failure semantics, observability, TUI panic recovery, logger discipline.

**Skills loaded**: `golang-pro`, `golang-1-26-release`, `golang-code-style`.
**Release notes consulted**: Go 1.26 release notes (specifically `errors.AsType`, `slog.NewMultiHandler`, `os/signal.NotifyContext`-cause propagation, `runtime` goroutine-leak profile).

---

## Executive summary

Marina's fan-out engine (`internal/actions/fetch.go`) models partial failure cleanly via `HostFetchResult`. Everything *downstream* of fetch regresses: `marina update --all --yes` and `marina prune --all` print warnings and return `nil`, so 9/10 failed hosts still exits 0 — that is the single largest reliability gap and it blocks cron/CI use cases. The TUI has no panic recovery on the `Update()` path (one bad `tea.Msg` crashes the dashboard and leaves the terminal in alt-screen). Observability is one-directional: everything lands in `~/.config/marina/marina.log` with no stderr echo and no `-v`/`--debug` flag. Error handling is mostly good (consistent `%w` wrapping, no panics outside `main`) but a handful of swallowed errors, one capitalized error string, and context-less TUI commands exist.

---

## Findings

### Partial-host failures in `update --all --yes` silently exit 0
- **Severity**: P0
- **Category**: reliability
- **Location**: `commands/updates.go:317` (and the whole body from `:244-316`)
- **Evidence**:
  ```go
  if err := runStackUpdateQuiet(cmd.Context(), cfg, k, stackDirs[k]); err != nil {
      report(ew, fmt.Sprintf("  [%s] %s: FAILED — %v", host, k.Stack, firstLine(err)))
      continue
  }
  ...
  return nil // at line 317, unconditionally
  ```
- **Why it matters**: Cron runs (`marina update --all --yes && ...`) and CI pipelines rely on the process exit code to gate downstream steps and to alert on failure. Today a full-cluster update where every single host failed still reports success. The README explicitly promises scriptable CLI output; exit-code correctness is part of that contract.
- **Recommendation**: Aggregate per-host/per-stack errors into a typed aggregate and return it so cobra surfaces non-zero. Mirror the `HostFetchResult` shape that `FetchAllHosts` already uses. Sketch:
  ```go
  type HostStackErr struct{ Host, Stack string; Err error }
  type ApplyErr struct{ Failures []HostStackErr }
  func (e *ApplyErr) Error() string { /* "N host(s) failed: …" */ }
  func (e *ApplyErr) Unwrap() []error { /* so errors.Is/As works over all failures */ }
  ```
  `errors.Unwrap() []error` is supported since Go 1.20; pair it with `errors.AsType` (Go 1.26) for type-safe inspection in tests. Collect `HostStackErr` values under a mutex-protected slice in the goroutine, then return `&ApplyErr{…}` when the slice is non-empty.
- **Effort**: M

### Partial-host failures in `prune --all` silently exit 0
- **Severity**: P0
- **Category**: reliability
- **Location**: `commands/prune.go:151-163`
- **Evidence**:
  ```go
  for _, hc := range targets {
      err := execWithSpinner(ctx, w, hc, /*…*/, dockerCmd, /*…*/)
      if err != nil {
          fmt.Fprintf(cmd.ErrOrStderr(), "warning: host %q: %v\n", hc.name, err)
      }
  }
  return nil
  ```
- **Why it matters**: Same contract as above — `marina prune --all -y` run from a cron script will claim success even when every host failed.
- **Recommendation**: Same aggregate-error pattern. Also: prune is destructive, so consider failing fast (first error) unless a `--keep-going` flag is explicitly passed.
- **Effort**: S

### Post-apply prune failures are lost inside `update`
- **Severity**: P1
- **Category**: reliability
- **Location**: `commands/updates.go:291-316`
- **Evidence**: `runHostPruneQuiet` errors are printed via `report(ew, …)` and the goroutine `return`s; nothing aggregates them, and the function ends at `:317 return nil`.
- **Why it matters**: Dangling-image accumulation is the whole point of `prune_after_update: true`. A silently failing prune pass (disk full, SSH hiccup) compounds over many `marina update` cycles and eventually wedges hosts out of disk — the exact failure this feature exists to prevent.
- **Recommendation**: Include prune failures in the same `ApplyErr` aggregate from the first finding.
- **Effort**: S

### TUI `Update()` has no panic recovery — one bad `tea.Msg` crashes the dashboard
- **Severity**: P1
- **Category**: reliability
- **Location**: `internal/tui/dashboard.go:41-77` (dashboard), and every screen's `Update` (`updates.go:124`, `stacks.go`, `hosts.go`, `prune.go:181`, `containers.go`)
- **Evidence**: dashboard.go:74-76 calls `m.top().Update(msg)` with no `defer recover()`; none of the individual screen `Update` methods guard either. Bubble Tea's runtime has some protection but a panic inside a screen's Update still terminates the program and leaves the alt-screen active on some terminals.
- **Why it matters**: Marina accepts `tea.Msg` values from arbitrary async commands — `ActionResultMsg`, `SequenceResultsMsg`, `pruneDoneMsg`, `checkerReadyMsg`, `updateResultMsg`. A nil-deref (e.g. `s.cfg.Hosts[k.host]` returning nil and code dereferencing `hostCfg.Something` — already a real risk, see `internal/tui/updates.go:562-563`) crashes the whole UI. With no recovery + alt-screen, users see a scrambled terminal and lose any in-flight state.
- **Recommendation**: Wrap the top-level dispatch in a recover that logs to the audit log and transitions to an error screen rather than crashing:
  ```go
  func (m *dashboard) Update(msg tea.Msg) (tea tea.Model, cmd tea.Cmd) {
      defer func() {
          if r := recover(); r != nil {
              Log().Error("tui.panic", "msg_type", fmt.Sprintf("%T", msg), "panic", r,
                  "stack", string(debug.Stack()))
              // swap top screen with a panic-recovery screen
          }
      }()
      // … existing body …
  }
  ```
  Pair with `GOEXPERIMENT=goroutineleakprofile` in debug builds (Go 1.26 experiment) to catch related leaks in panicking paths.
- **Effort**: S

### TUI commands run SSH on `context.Background()` — Ctrl+C leaks work
- **Severity**: P1
- **Category**: reliability
- **Location**: `internal/tui/actions.go:61` (`ComposeExecCmd`), `:85` (`rawExec`)
- **Evidence**:
  ```go
  out, err := actions.ComposeOp(context.Background(), sshCfg, dir, subCmd)
  ...
  func rawExec(sshCfg internalssh.Config, command string) (string, error) {
      return internalssh.Exec(context.Background(), sshCfg, command)
  }
  ```
- **Why it matters**: The dashboard is constructed with `ctx` (`run.go:15`, `dashboard.go:24`) and `tea.WithContext(ctx)` wires Ctrl+C / SIGTERM to the program. But these action commands detach from that ctx and use `Background()`, so pressing Ctrl+C kills the UI while the SSH session on the remote host continues until it finishes on its own. Orphaned `docker compose pull` processes on remote hosts are a known class of homelab footgun.
- **Recommendation**: Plumb the dashboard's ctx (already on every screen as `s.ctx`) into `ComposeExecCmd` / `DockerExecCmd` — change the signatures or attach via a closure:
  ```go
  func ComposeExecCmd(ctx context.Context, sshCfg internalssh.Config, …) tea.Cmd {
      return func() tea.Msg {
          out, err := actions.ComposeOp(ctx, sshCfg, dir, subCmd)
          …
      }
  }
  ```
  Callers already have `s.ctx` available. `internal/tui/prune.go:92` shows the right pattern (`pruneExecCmd(ctx, …)` already takes ctx explicitly).
- **Effort**: S

### No `-v`/`--debug` flag for fan-out observability
- **Severity**: P1
- **Category**: observability
- **Location**: `commands/root.go:15-21` (GlobalFlags has no verbosity field)
- **Evidence**: The `GlobalFlags` struct defines `Host`, `Stack`, `Container`, `Config`, `All` — no verbosity toggle. Fan-out timing info only lands in `~/.config/marina/marina.log`, and even that doesn't record per-host elapsed time for the fetch path (`internal/actions/fetch.go:71-83` logs nothing at all).
- **Why it matters**: "Why is `marina ps --all` slow?" is the exact question users ask once they have 10+ hosts. Today the only recourse is `tail -f ~/.config/marina/marina.log` in another pane, which won't help because the fetch path doesn't log. Cron jobs that stall can't be post-mortem'd without per-host timings.
- **Recommendation**: Add `--debug` and/or `-v` on root; gate a stderr `slog.Handler` behind it using Go 1.26's `slog.NewMultiHandler`:
  ```go
  handlers := []slog.Handler{fileHandler}
  if gf.Debug {
      handlers = append(handlers, slog.NewTextHandler(os.Stderr,
          &slog.HandlerOptions{Level: slog.LevelDebug}))
  }
  logger = slog.New(slog.NewMultiHandler(handlers...))
  ```
  Add `time.Since(start)` timing around each per-host fan-out in `fetchOneHost`. `slog.NewMultiHandler` is new in Go 1.26 — before that you needed a third-party adapter or a custom handler.
- **Effort**: M

### `fetchOneHost` has zero observability
- **Severity**: P1
- **Category**: observability
- **Location**: `internal/actions/fetch.go:71-83`
- **Evidence**: The function connects, lists, writes cache, and returns — no slog calls, no timing, no identification of whether the result came from live or fallback.
- **Why it matters**: Every other action layer (compose, prune, check) logs `start`/`ok`/`fail`. Fetch is the single hottest path in Marina (every `ps`, `stacks`, TUI refresh) and it's invisible. When users report "marina hangs with 10 hosts", there's no evidence trail.
- **Recommendation**: Add `Log().Info("fetch.start"/"fetch.ok"/"fetch.fail"/"fetch.cached", "host", host, "elapsed_ms", …)`. Requires the logger to be reachable from `actions` — either move the logger to its own package (`internal/log`) used by both `tui` and `actions`, or inject it. Moving is the cleaner long-term fix.
- **Effort**: M

### `state.SaveHostSnapshot` errors are swallowed
- **Severity**: P2
- **Category**: reliability
- **Location**: `internal/actions/fetch.go:79`
- **Evidence**:
  ```go
  _ = state.SaveHostSnapshot(host, &state.HostSnapshot{
      Containers: toStateContainers(containers),
  }, "")
  ```
- **Why it matters**: The cache is the offline-fallback mechanism. If every save silently fails (disk full, permissions, config path broken) the user has no way to know until the next outage reveals an empty cache. The comment at the top of the file makes the cache a correctness concern.
- **Recommendation**: Log on error at Warn level (once observability is wired up per findings above): `if err := state.SaveHostSnapshot(…); err != nil { Log().Warn("cache.save_fail", "host", host, "err", err) }`. Don't fail the fetch — save is still best-effort — but surface.
- **Effort**: S

### `registry.SaveCache` errors are swallowed
- **Severity**: P2
- **Category**: reliability
- **Location**: `commands/updates.go:460`
- **Evidence**: `_ = registry.SaveCache(cache, "")`
- **Why it matters**: Same pattern as above — silent cache-save failure degrades behavior across runs.
- **Recommendation**: Log, don't swallow. `if err := registry.SaveCache(cache, ""); err != nil { slog.Warn("registry.cache.save_fail", "err", err) }`.
- **Effort**: S

### TUI config.Save swallows error in "best-effort" purge path
- **Severity**: P2
- **Category**: reliability
- **Location**: `internal/tui/stacks.go:588`
- **Evidence**:
  ```go
  _ = config.Save(s.cfg, "") // best-effort; surface as error on next refresh if it failed
  ```
- **Why it matters**: The comment acknowledges the user won't know until later. If `config.Save` starts failing (permissions, disk full) and the user purges 5 stacks in a row, none of the entries get removed from config and the stacks keep showing up in listings. The existing audit log is the right place to surface this.
- **Recommendation**: `if err := config.Save(s.cfg, ""); err != nil { Log().Warn("purge.config_save_fail", "host", host, "stack", name, "err", err) }`.
- **Effort**: S

### Error string capitalized — violates Go convention
- **Severity**: P3
- **Category**: style (reliability-adjacent: error chains)
- **Location**: `internal/ssh/ssh.go:156`
- **Evidence**: `return nil, fmt.Errorf("SSH handshake %s: %w", addr, err)`
- **Why it matters**: Go convention (and `golangci-lint`'s `revive` `error-strings` rule) says error messages start lowercase so they concatenate cleanly. Downstream wrappers produce `"ssh exec: SSH handshake host:22: …"` — the capital S reads poorly mid-sentence.
- **Recommendation**: Change to `"ssh handshake %s: %w"`. No caller is doing exact-prefix matching on this string.
- **Effort**: S

### Dead `_ = fmt.Sprintf` in log helper
- **Severity**: P3
- **Category**: code hygiene
- **Location**: `internal/tui/log.go:77`
- **Evidence**:
  ```go
  if maxLen > 0 && len(s) > maxLen {
      s = s[:maxLen] + "…"
      _ = fmt.Sprintf // keep fmt import usable for future helpers
  }
  ```
- **Why it matters**: The comment admits this is load-bearing nothing. `fmt` isn't even used in this file — removing the line and the `fmt` import is clean. Dead code here is a minor signal reviewers learn to tolerate, and that tolerance spreads.
- **Recommendation**: Delete line 77 and the `"fmt"` import.
- **Effort**: S

### `len(s) > maxLen` counts bytes, not runes, when truncating
- **Severity**: P3
- **Category**: correctness
- **Location**: `internal/tui/log.go:75-77` (`shortenErr`)
- **Evidence**: `len(s) > maxLen` then `s = s[:maxLen] + "…"` — if the error message contains multi-byte UTF-8 (Docker sometimes emits non-ASCII in messages), the slice can cut mid-codepoint.
- **Why it matters**: Produces malformed UTF-8 in the audit log; some log readers choke. Low probability, easy fix.
- **Recommendation**: Use runes: `r := []rune(s); if len(r) > maxLen { s = string(r[:maxLen]) + "…" }`.
- **Effort**: S

### No use of `errors.Is`/`errors.As` beyond three `fs.ErrNotExist` checks
- **Severity**: P3
- **Category**: reliability (future-proofing)
- **Location**: whole tree — only three call sites: `internal/state/state.go:62`, `internal/registry/cache.go:52`, `internal/config/config.go:98`
- **Evidence**: `grep -rn 'errors.Is\|errors.As'` returns those three lines only. `commands/updates.go:553-560` instead uses `strings.Contains(err.Error(), "TOOMANYREQUESTS")` etc.
- **Why it matters**: `statusText` in updates.go branches on substrings of the wrapped error message — fragile to any upstream wording change. Once an `ApplyErr` aggregate exists (first finding), tests will want to assert "prune failed on host X" via `errors.As`, which won't work if error types aren't used.
- **Recommendation**: When adding the aggregate error type, define sentinel errors for the classified cases (`ErrRateLimited`, `ErrLocalBuild`, `ErrConnection`) in the registry package, and switch `statusText` to `errors.Is`. Go 1.26's `errors.AsType` is a nicer ergonomic wrapper.
- **Effort**: M

### `firstLine` in commands/updates.go byte-truncates at 79
- **Severity**: P3
- **Category**: correctness
- **Location**: `commands/updates.go:418-424`
- **Evidence**: `if len(s) > 80 { s = s[:79] + "…" }` — same multi-byte-cut bug as `shortenErr`.
- **Why it matters**: Identical to finding above; both should be fixed together.
- **Recommendation**: Rune-aware truncation; share one helper in a small `internal/strutil` package.
- **Effort**: S

---

## What Marina does *right* (so these don't get "fixed" away)

- `FetchAllHosts` (`internal/actions/fetch.go`) already models partial failure per host. Keep this shape — extend the pattern downstream.
- Audit log goes to `~/.config/marina/marina.log` (stderr-safe: nothing in the TUI or CLI prints logs to stdout, preserving `marina ps | cat` for cron). Logger discipline is good; the issue is one-directional, not dirty.
- Consistent `%w` wrapping throughout SSH/config/state/registry layers.
- No `panic()` or `recover()` anywhere in the tree — good. The gap is specifically at the TUI boundary, where incoming `tea.Msg` types can come from third-party code we don't control.
- `ssh.Exec` / `ssh.Stream` respect `ctx` via a watcher goroutine that closes the client on cancel (`internal/ssh/ssh.go:171-180`). Context discipline at the SSH layer is correct; it's only the TUI call sites that bypass it.

---

## Routed to teammates / cross-referenced

- **runtime-guy** flagged `internal/registry/check.go:57-60` (`BuildChecker` drops partial candidates on first host error). Confirmed: `gatherCandidates` correctly collects candidates from succeeding hosts and returns `(all, firstErr)`, but `BuildChecker` discards `all` in its `if err != nil { return nil, nil, nil, err }` guard. This is the same partial-failure-contract violation my P0 findings call out, one layer upstream — one failed host aborts the whole `marina check --all` pass. See **review/02-runtime.md** for the primary writeup; the fix belongs in the same aggregate-error refactor (return `(candidates, &CheckErr{…})` and let callers decide whether to proceed with the partial set).
