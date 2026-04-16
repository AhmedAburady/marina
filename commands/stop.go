package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newStopCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop a container or compose stack on a remote host",
		Example: `  marina stop -H myhost -c mycontainer
  marina stop -H myhost -s mystack`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if gf.Stack == "" && gf.Container == "" {
				return fmt.Errorf("specify -s <stack> or -c <container>")
			}

			hc, err := resolveHost(gf)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			ctx := cmd.Context()

			if gf.Container != "" {
				return execWithSpinner(ctx, w, hc,
					fmt.Sprintf("Stopping container %s on %s...", gf.Container, hc.name),
					"docker stop "+gf.Container,
					fmt.Sprintf("Container %q stopped on %q", gf.Container, hc.name),
				)
			}

			return execStackWithSpinner(ctx, w, hc, gf.Stack,
				fmt.Sprintf("Stopping stack %s on %s...", gf.Stack, hc.name),
				"cd %s && docker compose stop",
				fmt.Sprintf("Stack %q stopped on %q", gf.Stack, hc.name),
			)
		},
	}
}
