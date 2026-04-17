# Marina Review — Implementation Tasks

Source: `review/REVIEW.md` (2026-04-17). 45 findings across P0–P3.

**Decisions locked (2026-04-17):**
- `update --all --yes` → collect-all (aggregate typed error, non-zero exit)
- P0-2 → fast-flags path only (ssh hardening flags in connhelper); full connhelper replacement is out-of-scope
- P2-8 → one-release back-compat read fallback for old `~/.config/marina/` path
- P1-17F → include Windows + macOS in CI matrix (declared release targets)

---

## Wave 1 — Safety & independent hardening (parallel)

| # | Agent | Findings | Primary files |
|---|-------|----------|---------------|
| 1 | `atomic-writes` | P0-1, P1-10 | `internal/state/state.go`, `internal/config/config.go`, `internal/registry/cache.go`, `internal/actions/fetch.go` |
| 2 | `ssh-hardening` | P1-11, P2-13, P3-8, P3-10 | `internal/ssh/ssh.go` only |
| 3 | `connhelper-flags` | P0-2 (fast path) | `internal/docker/client.go` |
| 4 | `tui-ctx-threading` | P1-7 | `internal/tui/actions.go` + 7 call sites |
| 5 | `ci-release` | P1-17 (A–F), P2-9, P2-10 | `.github/workflows/*`, `.golangci.yml`, `dependabot.yml` |
| 6 | `small-independents` | P1-9, P1-13, P1-14 | `internal/registry/registry.go`, `internal/notify/gotify.go`, `internal/tui/dashboard.go` |

## Wave 2 — Engine completion (serialize on shared files)

| # | Agent | Findings | Primary files |
|---|-------|----------|---------------|
| 7 | `actions-stacks-consolidation` | P1-1, P1-5, P1-12 | `internal/actions/stacks.go`, `internal/tui/stacks.go` |
| 8 | `actions-updates-checks` | P1-2, P1-3, P1-8, P2-2, P2-3 | `commands/updates.go`, `internal/registry/check.go`, `internal/registry/cache.go`, `internal/actions/{updates,checks}.go` (new) |
| 9 | `commands-helpers` | P1-4, P1-6, P1-15 | `commands/helpers.go`, `commands/root.go`, `commands/{ps,stacks,updates,prune}.go` |

## Wave 3 — Error aggregation (depends on Wave 2)

| # | Agent | Findings | Primary files |
|---|-------|----------|---------------|
| 10 | `exit-code-aggregation` | P0-3 | `commands/updates.go`, `commands/prune.go`, `internal/actions/errors.go` (new) |

## Wave 4 — Test suite (staged, depends on Wave 2)

| # | Agent | Findings | Scope |
|---|-------|----------|-------|
| 11a | `tests-registry-discovery` | P1-16 stage 1–2 | `internal/registry`, `internal/discovery` |
| 11b | `tests-config-ssh` | P1-16 stage 3–4 | `internal/config`, `internal/ssh` (test server) |
| 11c | `tests-actions-cli` | P1-16 stage 5–6 | `internal/actions` (synctest), CLI golden |

## Wave 5 — Polish backlog (parallel, low-risk)

| # | Agent | Findings |
|---|-------|----------|
| 12 | `modernizers` | P3-1, P3-2, P3-3, P3-4, P3-5, P2-4 |
| 13 | `xdg-dirs` | P2-8 |
| 14 | `windows-ssh-agent` | P2-7 |
| 15 | `leftovers` | P2-1, P2-5, P2-6, P2-11, P3-6, P3-7, P3-9, P3-11, P3-12 |

---

## Status
- [ ] Wave 1 kicked off 2026-04-17
- [ ] Wave 2
- [ ] Wave 3
- [ ] Wave 4
- [ ] Wave 5
