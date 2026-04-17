# Marina Dashboard TUI

## Context

Today `marina <subcommand>` renders a **bordered lipgloss table** by default (rounded borders, colored header, host group headers) and a plain tabwriter fallback under `--plain`. Both carry the same information, so `--plain` is redundant. Meanwhile `marina` alone just prints cobra help — a missed opportunity for a cohesive, tab-driven full-screen experience.

**What changes:**

1. The **`--plain` flag is removed**. The bordered lipgloss table stays as the only output for every subcommand — that's the correct rendering. The plain tabwriter code path is the dead code that goes away.
2. **`marina` (no args, TTY)** launches a full-screen Bubble Tea dashboard with tabs for Hosts, Containers, Stacks, and Updates. All interactive management lives here.
3. **`marina check`**'s standalone Bubble Tea TUI (`internal/ui/checktui.go`) is folded into the dashboard's Updates tab. The subcommand keeps working but becomes the bordered-table output only.

**Core principle — reuse, don't rewrite.** Business logic (SSH exec, docker calls, stack discovery, state cache, registry checks, gotify notify) is already factored into `internal/*` packages. The dashboard is a new consumer — it does not reimplement any of it. Visual language copies the existing checktui.go palette so subcommand tables and the dashboard feel like one app.

## Library versions (verified in go.mod)

- `charm.land/bubbletea/v2 v2.0.2`
- `charm.land/bubbles/v2 v2.0.0`
- `charm.land/lipgloss/v2 v2.0.1`
- `charm.land/huh/v2` (cobra subcommands only — never inside the TUI)
- Go 1.26.2

## Bubble Tea v2 patterns to follow (firecrawl-verified)

These are the v2-current idioms confirmed from the tabs example, composable-views example, and the official upgrade guide. `internal/ui/checktui.go` already uses them — copy its patterns.

- `View() tea.View` (not `string`). Produce with `tea.NewView(s)` and set `v.AltScreen = true` declaratively. No `tea.EnterAltScreen` / `tea.WithAltScreen` calls.
- `Update(msg tea.Msg) (tea.Model, tea.Cmd)` — returns the `tea.Model` interface.
- Key presses: match `tea.KeyPressMsg`, not `tea.KeyMsg` (v2 made `tea.KeyMsg` an interface covering press + release).
- Tab navigation from the official tabs example: bind `tab`/`right`/`l`/`n` → next, `shift+tab`/`left`/`h`/`p` → prev. Render tab chrome with `lipgloss.JoinHorizontal` + a `lipgloss.Border` whose `BottomLeft`/`Bottom`/`BottomRight` chars are overridden per active/inactive state.
- Composable sub-models (composable-views example): parent dispatches global messages (window size, quit, tab-switch) itself, then delegates the rest to the active tab. Each sub-model keeps its own `spinner.Model` / `viewport.Model` and tick-forwards them.
- Shell out for logs: `tea.ExecProcess(exec.Cmd, func(err error) tea.Msg{...})`. Pauses the Program, resumes after the child exits — avoids reimplementing log streaming inside a viewport.
- Do **not** call `huh.Spinner`, `huh.NewForm`, or `ui.SelectHost` from inside a tab — they own stdin/stdout and deadlock against Bubble Tea. Use inline confirm modals rendered in `View()`.

## Scope Phasing

Three PRs. Each merges independently.

### PR A — Remove `--plain` flag, delete the tabwriter code path

Subcommands keep the bordered lipgloss table. Everything gated behind `--plain` goes.

**Files touched:**
- `commands/root.go` — delete the `Plain` field on `GlobalFlags` and its `BoolVar("plain", …)` registration.
- `commands/ps.go` — remove the `if gf.Plain { PrintContainerTablePlain } else { PrintContainerTable }` branches. Always call `ui.PrintContainerTable` (both the live path and the cached-fallback path).
- `commands/stacks.go` — same pattern: always `ui.PrintStackTable`.
- `commands/hosts.go` — in `runHostsList`, always build the `ui.StyledTable("NAME","USER","ADDRESS","KEY")`. In `newHostsTestCmd`, always build the `ui.StyledTable("HOST","STATUS","LATENCY")`. Delete the `tabwriter` branches and drop the `text/tabwriter` + `time.Duration` path parts no longer needed.
- `commands/updates.go` — delete the `if gf.Plain { … }` block (the entire plain-table branch, including the outer `spinner.New()` action that it owned). Keep the interactive TUI path as the default for `marina check`. `--notify` keeps its own branch.
- `internal/ui/table.go` — delete the plain helpers (`PrintContainerTablePlain`, `PrintStackTablePlain`, `PrintHostTablePlain`, `newPlainWriter`) and remove the now-unused `text/tabwriter` import. Keep everything else (`StyledTable`, `PrintContainerTable`, `PrintStackTable`, `hostHeader`, `containerName`, `formatPorts`, `stackStatus`, the style vars).
- `commands/updates.go` — also delete the `printUpdateTablePlain` helper. Keep `printUpdateTable` (the bordered version).

**Keep as-is:** `ui.SelectHost` (huh prompt is fine in a cobra subcommand), action spinners (`execWithSpinner`, `execStackWithSpinner`), `internal/ui/checktui.go` (moves in PR C).

**Verify:** `go build ./...`, `go vet ./...`. Run each subcommand and confirm the bordered table renders:
- `marina ps -H <host>` → bordered container table
- `marina stacks -H <host>` → bordered stack table
- `marina hosts` → bordered host list
- `marina hosts test` → bordered connectivity table
- `marina check -H <host>` → existing interactive check TUI (untouched)
- `marina ps --plain` → fails with "unknown flag: --plain" (intended break)

### PR B — Dashboard scaffold + Hosts tab

**Goal:** prove the architecture with one read-only tab before wiring up actions.

**New package: `internal/tui/`** (sibling to `internal/ui/`; legacy table helpers stay in `ui`, dashboard code lives in `tui`).

- `internal/tui/dashboard.go` — top-level `model`. Fields: `cfg *config.Config`, `tabs []Tab`, `active int`, `width/height int`, shared palette. Implements `Init/Update/View` per bubbletea v2. Handles global keys (`q`/`ctrl+c` quit, `tab`/`shift+tab` switch, `1-4` jump directly, `r` forwards a `refreshMsg` to the active tab). All other messages delegated to `m.tabs[m.active].Update`.
- `internal/tui/tabs.go` — tab-bar renderer copied from the bubbletea `examples/tabs` pattern (active/inactive border corners via `tabBorderWithBottom`). Defines the `Tab` interface:
  ```go
  type Tab interface {
      Title() string
      Init() tea.Cmd
      Update(tea.Msg) (Tab, tea.Cmd)
      View(width, height int) string
      Help() string // one-line keybinding hint
  }
  ```
- `internal/tui/styles.go` — lift the palette from `internal/ui/checktui.go` (`cAccent #7D56F4`, `cTeal`, `cGreen`, `cYellow`, `cDim`, `cFg`, `sWindow`, `sTitle`, `sHost`, `sHeader`, `sRow`, `sCursor`, `sHelp`, `sCheck`) into this file so both the dashboard and (in PR C) the Updates tab share the exact same styles.
- `internal/tui/hosts_tab.go` — first tab. Columns NAME/USER/ADDRESS/KEY pulled from `cfg.Hosts`. Keybinding `t` starts a concurrent connectivity test: fires N async `tea.Cmd`s, each running `internalssh.Exec(ctx, sshCfg, "echo ok")`, returning a `hostTestResultMsg{host, ok, latency, err}`. The row shows a `bubbles/spinner` while running and latency-or-error once resolved.
- `commands/root.go` — add `RunE` on the root command. When `len(args) == 0` and stdin+stdout are TTYs (`github.com/charmbracelet/x/term.IsTerminal(os.Stdout.Fd())`), call `tui.Run(ctx, cfg)` and return; otherwise fall through to `cmd.Help()`. Verify the root currently has no `RunE`/`Run` so adding one won't collide (it doesn't today).

**Architecture constraint (reiterated):** tab actions fire a `tea.Cmd` that wraps raw `internalssh.Exec` / `docker.Client` calls. Never call `execWithSpinner`, `execStackWithSpinner`, or `ui.SelectHost` from a tab.

**Verify:** `marina` opens the full-screen dashboard on Hosts. `marina | cat` prints help. `marina ps` unchanged. `q`/`ctrl+c` exit cleanly. Pressing `t` populates latencies row-by-row.

### PR C — Containers, Stacks, Updates tabs with actions

**New files:**
- `internal/tui/containers_tab.go`
- `internal/tui/stacks_tab.go`
- `internal/tui/updates_tab.go` *(absorbs `internal/ui/checktui.go`)*
- `internal/tui/actions.go` — shared `tea.Cmd` builders:
  ```go
  func dockerExecCmd(sshCfg internalssh.Config, command string, kind, target string) tea.Cmd
  func composeExecCmd(sshCfg internalssh.Config, dir, command string, kind, target string) tea.Cmd
  ```
  Each runs `internalssh.Exec` and returns an `actionResultMsg{kind, target, output, err}`.

**Containers tab:**
- Extract the fan-out in `commands/ps.go:runPs` into a reusable `tui.fetchAllHosts(ctx, cfg) (map[string][]container.Summary, error)` helper. Wrap it in a `tea.Cmd`.
- Rows grouped by host, reusing the existing `ui.containerName` and `ui.formatPorts` formatters.
- Keys: `s` start, `x` stop, `r` restart, `l` logs, `↑/↓` move, `enter` toggle an inspect detail panel.
- Logs: `tea.ExecProcess(exec.Command(os.Args[0], "logs", "-H", host, "-c", container, "-f"), …)` suspends the TUI; `ctrl+c` returns.

**Stacks tab:**
- Loads containers via `tui.fetchAllHosts`, then `discovery.GroupByStack(host, containers, cfgStacks)` per host — same pipeline as `commands/stacks.go:runStacks`.
- Rows: NAME / DIR / RUNNING/TOTAL.
- Keys: `s` start (`docker compose up -d`), `x` stop (`docker compose stop`), `r` restart, `p` pull, `u` update (pull + up -d), `P` purge (inline confirm modal), `enter` expand into child containers.
- Purge reuses the three-step sequence from `commands/stacks.go:newStacksPurgeCmd` (compose down → `rm -rf dir` → config removal → image prune), each step wrapped in a sequential `tea.Cmd`.

**Updates tab (absorbs checktui.go):**
- Move `internal/ui/checktui.go` → `internal/tui/updates_tab.go`. Adapt the model to satisfy the `Tab` interface rather than being a standalone `tea.Program`. Keep all keybindings (`j/k`, `space`, `a`, `t`, `enter`). Scope `q` to "leave tab" (dashboard `q` handles program quit).
- Extract candidate gathering + dedup + persistent-cache check (`commands/updates.go` lines 85–159) into `internal/registry.BuildChecker(ctx, cfg) (candidates []UpdateCandidate, checkFn func(…) UpdateCheckResult, err error)`. Both `marina check` (cobra) and the dashboard tab consume the same helper.
- Apply step: the same `docker compose pull && docker compose up -d` sequence, wrapped in a `tea.Cmd`.

**Files modified:**
- `commands/updates.go` — remove the `ui.RunCheckTUI` branch. `marina check` now goes straight to the bordered-table output (the existing `printUpdateTable` path, already present).
- `internal/ui/checktui.go` — deleted once `updates_tab.go` replaces it.
- `internal/registry/` — gains `BuildChecker` (either in `registry.go` or a small new `check.go`).

**Verify end-to-end:**
1. `go build ./...` clean; `go test ./...` green.
2. `marina` opens dashboard; tab through Hosts → Containers → Stacks → Updates.
3. Containers tab: select a test container, press `x`, watch inline spinner, status flips to `exited`. Press `s` to start.
4. Stacks tab: press `p`; spinner resolves. Press `P`, confirm modal, stack purges.
5. Updates tab: checks run concurrently; select one, press `enter`, stack pulls and recreates.
6. Press `l` on a container row → TUI suspends, logs stream, `ctrl+c` → back to dashboard.
7. `marina ps --all`, `marina stacks`, `marina hosts`, `marina check` all print bordered tables (CLI path unchanged).
8. `marina | cat` prints help.

## Critical files to reference

- `commands/ps.go:35-127` — fan-out + state-cache fallback (reuse in containers tab)
- `commands/stacks.go:137-232` — stacks aggregation + cache fallback (reuse in stacks tab)
- `commands/stacks.go:234-335` — purge sequence (reuse in stacks-tab purge action)
- `commands/updates.go:69-348` — candidate gathering, registry dedup cache, TUI-selection apply (extract into `registry.BuildChecker`; reused in Updates tab)
- `commands/helpers.go:85-142` — `execWithSpinner` / `execStackWithSpinner` (reference only — not callable from tabs; the underlying `internalssh.Exec` is what the tab's `tea.Cmd` wraps)
- `internal/discovery/discovery.go` — `GroupByStack` reused as-is
- `internal/state/state.go` — cache fallback unchanged
- `internal/registry/registry.go` + `LoadCache`/`SaveCache` — reused as-is
- `internal/ui/checktui.go` — visual template and code source for the Updates tab (moved, not copied)
- `internal/ui/table.go` — `StyledTable`, `PrintContainerTable`, `PrintStackTable`, `containerName`, `formatPorts`, `stackStatus` all kept
- Bubble Tea v2 references in `.firecrawl/`: `bt-v2-tabs-example.md`, `bt-v2-composable-example.md`, `bubbletea-upgrade-guide.md`, `bt-v2-readme.md`

## Non-goals

- No refactor of `internal/ssh` or `internal/docker`.
- No new config fields. Dashboard reads existing `~/.config/marina/config.yaml`.
- No auto-refresh timer. Manual `r` only.
- No mouse support in PR B/C (keyboard-only; can add later via `tea.WithMouseCellMotion`).
- Pending actions don't block tab switching — each tab keeps its own pending set; status bar shows a global "N ops running" counter.
