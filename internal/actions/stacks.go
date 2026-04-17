package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/AhmedAburady/marina/internal/config"
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
func ComposeOp(ctx context.Context, sshCfg internalssh.Config, dir, subCmd string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("stack directory is empty — cannot run compose %q", subCmd)
	}
	cmd := fmt.Sprintf("cd %s && docker compose %s", dir, subCmd)
	return internalssh.Exec(ctx, sshCfg, cmd)
}

// ContainerOp runs a raw `docker <verb> <id>` on the remote host. Verbs:
// "start", "stop", "restart", "kill", etc.
func ContainerOp(ctx context.Context, sshCfg internalssh.Config, verb, containerID string) (string, error) {
	if containerID == "" {
		return "", fmt.Errorf("container id is empty")
	}
	return internalssh.Exec(ctx, sshCfg, fmt.Sprintf("docker %s %s", verb, containerID))
}

// ── Purge ───────────────────────────────────────────────────────────────────

// PurgeStep describes one step of a stack purge — compose-down → rm-rf →
// image-prune → config-save. Callers iterate these for per-step UI feedback.
type PurgeStep struct {
	Kind string // "compose.down", "dir.rm", "image.prune", "config.save"
	Run  func() error
}

// PurgePlan returns the ordered set of steps to completely remove a stack.
// The plan captures everything the CLI's `marina stacks purge` used to do
// inline, exposed as a reusable list so the TUI can drive the exact same
// sequence with per-step UI updates.
func PurgePlan(ctx context.Context, cfg *config.Config, configPath, host, stack string) ([]PurgeStep, error) {
	sshCfg := HostSSHConfig(cfg, host)
	if sshCfg == nil {
		return nil, fmt.Errorf("host %q not found", host)
	}
	dir, err := FindStackDir(ctx, cfg, host, stack)
	if err != nil {
		return nil, err
	}
	quotedDir := shellQuote(dir)

	steps := []PurgeStep{
		{
			Kind: "compose.down",
			Run: func() error {
				_, err := ComposeOp(ctx, *sshCfg, dir, "down --remove-orphans")
				return err
			},
		},
		{
			Kind: "dir.rm",
			Run: func() error {
				if quotedDir == "" {
					return fmt.Errorf("refusing to rm-rf: dir contains quote or backslash")
				}
				_, err := internalssh.Exec(ctx, *sshCfg, "rm -rf "+quotedDir)
				return err
			},
		},
		{
			Kind: "image.prune",
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

// shellQuote wraps a path in single quotes for safe shell use. Paths with
// embedded quotes or backslashes are rejected (return empty) so we never
// issue a malformed command to the remote shell.
func shellQuote(s string) string {
	if strings.ContainsAny(s, "'\"\\") {
		return ""
	}
	return "'" + s + "'"
}
