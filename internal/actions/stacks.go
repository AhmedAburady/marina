package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/discovery"
	"github.com/AhmedAburady/marina/internal/docker"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// RegisterStack writes a stack entry into the host's config so the stack
// appears in listings even when its containers are stopped. Returns an
// error if the name is already taken on that host.
func RegisterStack(cfg *config.Config, configPath, host, name, dir string) error {
	h, ok := cfg.Hosts[host]
	if !ok {
		return fmt.Errorf("host %q not found", host)
	}
	if name == "" || dir == "" {
		return fmt.Errorf("both name and dir are required")
	}
	if h.Stacks == nil {
		h.Stacks = make(map[string]string)
	}
	if _, exists := h.Stacks[name]; exists {
		return fmt.Errorf("stack %q already registered on host %q", name, host)
	}
	h.Stacks[name] = dir
	return config.Save(cfg, configPath)
}

// UnregisterStacks removes stack entries from a host's config. Returns the
// names that were actually removed and those not found. Config is persisted
// once if at least one removal succeeded.
func UnregisterStacks(cfg *config.Config, configPath, host string, names ...string) (removed, notFound []string, err error) {
	h, ok := cfg.Hosts[host]
	if !ok {
		err = fmt.Errorf("host %q not found", host)
		return
	}
	for _, n := range names {
		if _, ok := h.Stacks[n]; !ok {
			notFound = append(notFound, n)
			continue
		}
		delete(h.Stacks, n)
		removed = append(removed, n)
	}
	if len(removed) > 0 {
		err = config.Save(cfg, configPath)
	}
	return
}

// FindStackDir resolves the compose working directory for a stack on a host.
// Checks config-registered stacks first, then container labels.
func FindStackDir(ctx context.Context, cfg *config.Config, host, stack string) (string, error) {
	h, ok := cfg.Hosts[host]
	if !ok {
		return "", fmt.Errorf("host %q not found", host)
	}
	if dir, ok := h.Stacks[stack]; ok {
		return dir, nil
	}
	sshCfg := internalssh.Config{
		Address: h.SSHAddress(cfg.Settings.Username),
		KeyPath: h.ResolvedSSHKey(cfg.Settings.SSHKey),
	}
	dc, err := docker.NewClient(ctx, sshCfg.Address, sshCfg.KeyPath)
	if err != nil {
		return "", fmt.Errorf("connect to discover stack dir: %w", err)
	}
	defer dc.Close()
	containers, err := dc.ListContainers(ctx)
	if err != nil {
		return "", fmt.Errorf("list containers to discover stack dir: %w", err)
	}
	for _, c := range containers {
		if c.Labels["com.docker.compose.project"] == stack {
			if dir := c.Labels["com.docker.compose.project.working_dir"]; dir != "" {
				return dir, nil
			}
		}
	}
	return "", fmt.Errorf(
		"stack %q not found on host %q; register it with: marina stacks add %s <dir> -H %s",
		stack, host, stack, host,
	)
}

// ComposeOp runs `cd <dir> && docker compose <subCmd>` on the remote host.
// subCmd examples: "restart", "up -d", "stop", "pull", "down --remove-orphans".
// Returns the combined stdout+stderr plus any SSH/exit error.
//
// dir is single-quoted via ShellQuote before interpolation. Paths containing
// single quotes, double quotes, or backslashes are rejected outright to
// prevent shell injection via a malicious compose label.
func ComposeOp(ctx context.Context, sshCfg internalssh.Config, dir, subCmd string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("stack directory is empty — cannot run compose %q", subCmd)
	}
	q := ShellQuote(dir)
	if q == "" {
		return "", fmt.Errorf("refusing compose in %q: unsafe shell characters in directory path", dir)
	}
	cmd := fmt.Sprintf("cd %s && docker compose %s", q, subCmd)
	return internalssh.Exec(ctx, sshCfg, cmd)
}

// ContainerOp runs a raw `docker <verb> <id>` on the remote host. Verbs:
// "start", "stop", "restart", "kill", etc.
//
// containerID is validated against Docker's allowed identifier characters
// ([a-zA-Z0-9][a-zA-Z0-9_.-]*) before interpolation into the shell command.
// This covers both full hex IDs and human-friendly container names while
// rejecting shell metacharacters ($, `;`, backticks, etc.).
func ContainerOp(ctx context.Context, sshCfg internalssh.Config, verb, containerID string) (string, error) {
	if containerID == "" {
		return "", fmt.Errorf("container id is empty")
	}
	if !isValidContainerID(containerID) {
		return "", fmt.Errorf("refusing docker %s: invalid container id %q", verb, containerID)
	}
	return internalssh.Exec(ctx, sshCfg, fmt.Sprintf("docker %s %s", verb, containerID))
}

// isValidContainerID reports whether s is a safe Docker container identifier.
// Docker container names and IDs allow [a-zA-Z0-9_.-] with no leading dot or
// dash; this validates the full set so both short hex IDs and named containers
// are accepted while shell metacharacters are rejected.
func isValidContainerID(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case (c == '_' || c == '.' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// ── Purge ───────────────────────────────────────────────────────────────────

// PurgeStep describes one step of a stack purge — compose-down → rm-rf →
// image-prune → config-save. Callers iterate these for per-step UI feedback.
//
// For compose steps (Kind == "compose.down"), Dir and SubCmd carry the
// compose working directory and subcommand so the TUI can route through
// ComposeExecCmd for proper audit logging.
//
// For raw shell steps (Kind == "dir.rm", "image.prune"), Command carries the
// full shell command string so the TUI can route through DockerExecCmd.
//
// Run executes the step as a plain Go call (used by the CLI). TUI callers
// should prefer building a tea.Cmd from the data fields for richer logging.
type PurgeStep struct {
	Kind    string // "compose.down", "dir.rm", "image.prune", "config.save"
	Dir     string // compose working dir (compose.down only)
	SubCmd  string // compose subcommand (compose.down only)
	Command string // raw shell command (dir.rm, image.prune)
	Run     func() error
}

// PurgePlan returns the ordered set of steps to completely remove a stack.
// The plan captures everything the CLI's `marina stacks purge` used to do
// inline, exposed as a reusable list so the TUI can drive the exact same
// sequence with per-step UI updates.
//
// This wrapper performs an SSH round-trip via FindStackDir to locate the
// compose working directory. Callers that already know the dir (e.g. the
// TUI, which has it from the displayed row) should call PurgePlanFromDir
// directly to avoid the network call on the hot path.
func PurgePlan(ctx context.Context, cfg *config.Config, configPath, host, stack string) ([]PurgeStep, error) {
	dir, err := FindStackDir(ctx, cfg, host, stack)
	if err != nil {
		return nil, err
	}
	return PurgePlanFromDir(ctx, cfg, configPath, host, stack, dir)
}

// PurgePlanFromDir is the core plan builder. It does NOT issue any network
// calls — all SSH work is deferred to PurgeStep.Run closures so the caller
// controls concurrency (in particular the TUI builds the step list on the
// BubbleTea goroutine and must not block it with I/O).
func PurgePlanFromDir(ctx context.Context, cfg *config.Config, configPath, host, stack, dir string) ([]PurgeStep, error) {
	sshCfg := HostSSHConfig(cfg, host)
	if sshCfg == nil {
		return nil, fmt.Errorf("host %q not found", host)
	}
	if dir == "" {
		return nil, fmt.Errorf("stack directory is empty for %s/%s", host, stack)
	}
	quotedDir := ShellQuote(dir)

	steps := []PurgeStep{
		{
			Kind:   "compose.down",
			Dir:    dir,
			SubCmd: "down --remove-orphans",
			Run: func() error {
				_, err := ComposeOp(ctx, *sshCfg, dir, "down --remove-orphans")
				return err
			},
		},
		{
			Kind:    "dir.rm",
			Command: "rm -rf " + quotedDir,
			Run: func() error {
				if quotedDir == "" {
					return fmt.Errorf("refusing to rm-rf: dir contains quote or backslash")
				}
				_, err := internalssh.Exec(ctx, *sshCfg, "rm -rf "+quotedDir)
				return err
			},
		},
		{
			Kind:    "image.prune",
			Command: "docker image prune -f",
			Run: func() error {
				_, err := internalssh.Exec(ctx, *sshCfg, "docker image prune -f")
				return err
			},
		},
		{
			Kind: "config.save",
			Run: func() error {
				h := cfg.Hosts[host]
				if h == nil || h.Stacks == nil {
					return nil
				}
				if _, ok := h.Stacks[stack]; !ok {
					return nil
				}
				delete(h.Stacks, stack)
				return config.Save(cfg, configPath)
			},
		},
	}
	return steps, nil
}

// ShellQuote wraps a path in single quotes for safe shell use. Paths with
// embedded single quotes, double quotes, or backslashes are rejected
// (return empty) so we never issue a malformed command to the remote shell.
// Callers must treat an empty return as a hard error.
func ShellQuote(s string) string {
	if strings.ContainsAny(s, "'\"\\") {
		return ""
	}
	return "'" + s + "'"
}

// StackGroupsFor groups a host's running containers into stacks, merging in
// any manually configured stacks from configStacks. It is a thin forwarder
// over discovery.GroupByStack so that callers in commands/ and internal/tui/
// do not need to import internal/discovery directly.
func StackGroupsFor(host string, containers []container.Summary, configStacks map[string]string) []discovery.Stack {
	return discovery.GroupByStack(host, containers, configStacks)
}
