// Command marina is a multi-host Docker management tool.
// It connects to remote Docker hosts over SSH with zero agent setup required.
package main

import (
	"context"
	"os"
	"syscall"

	"charm.land/fang/v2"
	"github.com/AhmedAburady/marina/commands"
)

// version is set at build time via: -ldflags "-X main.version=v0.1.0"
var version = "dev"

func main() {
	root := commands.NewRootCmd(version)
	if err := fang.Execute(context.Background(), root,
		fang.WithVersion(version),
		fang.WithNotifySignal(os.Interrupt, syscall.SIGTERM),
	); err != nil {
		os.Exit(1)
	}
}
