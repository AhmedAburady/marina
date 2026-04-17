package commands

import (
	"context"
	"fmt"
	"io"

	"charm.land/huh/v2/spinner"
	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// hostContext holds the resolved config and SSH connection info for a target host.
// It is built once per command and passed to helpers.
type hostContext struct {
	cfg    *config.Config
	host   *config.HostConfig
	sshCfg internalssh.Config
	name   string // host name from config
}

// resolveHost validates the -H flag, loads config, and returns a hostContext.
func resolveHost(gf *GlobalFlags) (*hostContext, error) {
	if gf.Host == "" {
		return nil, fmt.Errorf("host is required: use -H <host>")
	}

	cfg, err := config.Load(gf.Config)
	if err != nil {
		return nil, err
	}

	h, ok := cfg.Hosts[gf.Host]
	if !ok {
		return nil, fmt.Errorf("host %q not found", gf.Host)
	}

	return &hostContext{
		cfg:  cfg,
		host: h,
		name: gf.Host,
		sshCfg: internalssh.Config{
			Address: h.SSHAddress(cfg.Settings.Username),
			KeyPath: h.ResolvedSSHKey(cfg.Settings.SSHKey),
		},
	}, nil
}

// findStackDir resolves the compose working directory for a stack on the
// host described by hc. This is a thin wrapper around actions.FindStackDir
// that adapts the hostContext-flavoured CLI surface to the cfg+host shared
// implementation. The underlying docker.NewClient call still inherits
// MaxConnsPerHost: 1 from internal/docker, so concurrent fallbacks serialize
// through a single SSH pipe per host.
func findStackDir(ctx context.Context, hc *hostContext, stackName string) (string, error) {
	return actions.FindStackDir(ctx, hc.cfg, hc.name, stackName)
}

// execWithSpinner runs an SSH command with a spinner, then prints the output.
// The title is shown while the command runs (e.g. "Restarting stack komodo on dockerworld...").
// The done message is printed after success (e.g. "Restarted stack \"komodo\" on \"dockerworld\"").
func execWithSpinner(ctx context.Context, w io.Writer, hc *hostContext, title, command, doneMsg string) error {
	var output string
	var execErr error

	spinErr := spinner.New().
		Type(spinner.MiniDot).
		Title(title).
		Action(func() {
			output, execErr = internalssh.Exec(ctx, hc.sshCfg, command)
		}).
		Run()
	if spinErr != nil {
		return spinErr
	}
	if execErr != nil {
		return execErr
	}

	fmt.Fprintln(w)
	if output != "" {
		fmt.Fprint(w, output)
	}
	fmt.Fprintln(w, doneMsg)
	return nil
}

// execStackWithSpinner resolves the stack dir, then runs an SSH command with a spinner.
// The commandFmt should contain a single %s for the directory, e.g. "cd %s && docker compose restart".
func execStackWithSpinner(ctx context.Context, w io.Writer, hc *hostContext, stackName, title, commandFmt, doneMsg string) error {
	var output string
	var execErr error

	spinErr := spinner.New().
		Type(spinner.MiniDot).
		Title(title).
		Action(func() {
			dir, err := findStackDir(ctx, hc, stackName)
			if err != nil {
				execErr = err
				return
			}
			output, execErr = internalssh.Exec(ctx, hc.sshCfg, fmt.Sprintf(commandFmt, dir))
		}).
		Run()
	if spinErr != nil {
		return spinErr
	}
	if execErr != nil {
		return execErr
	}

	fmt.Fprintln(w)
	if output != "" {
		fmt.Fprint(w, output)
	}
	fmt.Fprintln(w, doneMsg)
	return nil
}
