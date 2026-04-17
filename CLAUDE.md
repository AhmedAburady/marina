# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

**Marina** — a multi-host Docker management CLI written in Go. Manages containers and Docker Compose stacks across remote homelab hosts over SSH. Zero agent setup required on target hosts; uses Docker's native SSH connhelper (`docker system dial-stdio`) for all Docker API traffic.

## Commands

```bash
# Build
go build -ldflags "-X main.version=v0.1.0" ./cmd/marina

# Run directly
go run ./cmd/marina                 # bare invocation opens the full-screen dashboard
go run ./cmd/marina <subcommand>    # subcommands print bordered tables (scriptable)

# Run a specific test (no test suite exists yet)
go test ./internal/<package>/...

# Lint (if golangci-lint is available)
golangci-lint run

# Git helpers (justfile)
just push       # push to all remotes
just diff       # diff Go files only
just diff-main  # diff vs main branch
```

Bare `marina` only launches the dashboard when stdin/stdout are TTYs; piping it (`marina | cat`) falls through to cobra help.

## Architecture

```
cmd/marina/main.go          # Entry: fang wraps cobra root command
commands/                   # One file per CLI command (ps, stacks, prune, update, check, ...)
  root.go                   # GlobalFlags struct — shared -H/-s/-c/--all flags; RunE on root opens the TUI when no subcommand is given + stdout is a TTY
  helpers.go                # resolveHost(), hostContext, execWithSpinner
internal/
  config/    # YAML config at ~/.config/marina/config.yaml; per-host SSH + stack overrides
  ssh/       # Low-level SSH client: Exec() (one-shot) and Stream() (log tailing)
  docker/    # Docker client over SSH connhelper; auto-negotiates API version; ImageMeta struct with full RepoDigests list
  discovery/ # Groups containers by compose project labels → Stack structs
  state/     # JSON snapshot cache at ~/.config/marina/state.json (offline fallback)
  registry/  # Image digest HEAD checks (no-pull cost); in-cycle dedup only — no persistent cache
  notify/    # Gotify push notifications
  actions/   # SHARED business logic — FetchAllHosts, Prune, ComposeOp, ContainerOp, etc. Both CLI and TUI call through this layer; no duplication
  ui/        # Bordered lipgloss tables (`PrintContainerTable`, `PrintStackTable`) + huh selector (`SelectHost`) for CLI output
  tui/       # Full-screen Bubble Tea dashboard: screen stack, Home, Hosts, Containers, Stacks, Updates, Prune. Shared filter bar + confirm prompt live here
```

### Key Patterns

**GlobalFlags** (`commands/root.go`) — every command receives these flags: `-H` (host filter), `-s` (stack filter), `-c` (container filter), `--all` (all hosts). `resolveHost()` / host selector logic in `helpers.go` translates these into a `hostContext`. Root command's `RunE` opens the TUI when `len(args) == 0 && isTTY`.

**SSH execution** (`internal/ssh/ssh.go`) — `Exec()` returns combined stdout+stderr as a string; `Stream()` pipes output line-by-line for logs. Auth tries SSH agent (`SSH_AUTH_SOCK`) first, then key file. `known_hosts` verification is always enforced — never skip it.

**Docker connectivity** (`internal/docker/client.go`) — `NewClient()` takes a `HostConfig` and dials through SSH using Docker CLI's connhelper. `MaxConnsPerHost: 1` on the transport forces serialization through one SSH pipe (spawning multiple SSH subprocesses crashes on some hosts). `InspectContainer` returns `ImageMeta{Ref, Digests []string, Architecture, OS}` — the slice is critical: Docker accumulates every registry digest the image has ever answered to, and the registry check matches against any of them.

**Stack discovery** (`internal/discovery/discovery.go`) — `GroupByStack()` inspects running containers for `com.docker.compose.project` labels, merges with manually configured stacks (catches stopped stacks), and returns sorted `[]Stack` (running first).

**State cache** (`internal/state/state.go`) — saves container snapshots after successful queries; loaded as fallback when a host is unreachable. Keeps UX functional even during partial outages.

**Registry checks** (`internal/registry/registry.go`) — compares local image digests vs remote via `go-containerregistry` HEAD request. **No persistent cache** (only an in-cycle `sync.Map` so multiple containers sharing one image share one HEAD within a single check pass). `IsPinnedRef(ref)` short-circuits digest-pinned refs (`@sha256:…`) because they're an intentional user choice. Skips non-running containers at gather time — `c.State != "running"` → no candidate, no HEAD.

**Shared actions layer** (`internal/actions/`) — every CLI/TUI operation routes through here: `FetchAllHosts`, `Prune`, `ComposeOp`, `ContainerOp`, `RegisterStack`, etc. Changing business logic in one place moves both the CLI command and the TUI screen together. If you find yourself writing the same SSH/docker call twice in `commands/` and `internal/tui/`, push it into `actions/` first.

**TUI shared components** (`internal/tui/`):
- `filter.go` — `/`-activated substring filter, wired into every list screen
- `confirm.go` — single `confirmPrompt` with `Update`/`View` methods, used by Hosts delete, Containers remove, Stacks purge/unregister, Prune apply
- `overlay.go` — `overlayModal(bg, modal, w, h)` composites a modal dead-centre over a screen's View via lipgloss `Compositor`
- `actions.go` — `DockerExecCmd`, `ComposeExecCmd`, `SequenceCmds` (stop-on-error sequence). Every mutation logs start/ok/fail to the audit log

**Audit log** (`internal/tui/log.go`) — every TUI action writes `start` / `ok` / `fail` to `~/.config/marina/marina.log` via `slog`. Essential for diagnosing "apply did nothing" cases — tail it in another pane while testing.

## Adding a New Command

1. **Put business logic in `internal/actions/`** first — a pure function that takes `ctx`, an `ssh.Config`, and operation args; returns `(string, error)`. Both CLI and TUI will call this.
2. Create `commands/<name>.go` with a `New<Name>Cmd(gf *GlobalFlags)` constructor that calls the action.
3. Register it in `commands/root.go` under `rootCmd.AddCommand(...)`.
4. Use `resolveHost(gf)` / the interactive selector for host targeting; use `execWithSpinner` / `execStackWithSpinner` from `helpers.go` for progress UX.
5. Use `ui.PrintContainerTable` / `ui.PrintStackTable` / a bespoke `ui.StyledTable()` for output.

## Adding a New TUI Screen

1. Create `internal/tui/<name>.go`.
2. Implement the `Screen` interface: `Title / Init / Update / View / Help`.
3. Optional interfaces: `ModalProvider` (for confirm dialogs + forms), `ProgressReporter` (for long-running work that should surface in the terminal chrome progress bar).
4. Call into `internal/actions/` for SSH/docker work — don't reimplement the business logic.
5. Register in `internal/tui/home.go` under `items`.
6. Reuse `filterBar` (filter.go), `confirmPrompt` (confirm.go), `inlineForm` (form.go), and the scrolling/column helpers in `list.go` / `screen.go` — don't hand-roll new table layouts.

## Config Format

See `config.yaml.example`. Config lives at `~/.config/marina/config.yaml`. The `Settings` block provides SSH defaults + behavior toggles; each host entry can override `User` and `SSHKey`:

```yaml
hosts:
  <name>:
    address: <host or IP>
    user: <override>             # optional
    ssh_key: <path override>     # optional
    stacks:                      # optional — surfaces stopped stacks
      <stack>: <remote dir>

settings:
  username: <default user>
  ssh_key: <default key path>
  prune_after_update: true       # honored by both `marina update` and TUI Updates apply

notifications:
  gotify:                        # optional, only for `marina check --notify`
    url: <gotify URL>
    token: <app token>
    priority: <int>
```
