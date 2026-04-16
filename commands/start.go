package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newStartCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start a container or compose stack on a remote host",
		Example: `  marina start -H myhost -c mycontainer
  marina start -H myhost -s mystack`,
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
					fmt.Sprintf("Starting container %s on %s...", gf.Container, hc.name),
					"docker start "+gf.Container,
					fmt.Sprintf("Container %q started on %q", gf.Container, hc.name),
				)
			}

			return execStackWithSpinner(ctx, w, hc, gf.Stack,
				fmt.Sprintf("Starting stack %s on %s...", gf.Stack, hc.name),
				"cd %s && docker compose up -d",
				fmt.Sprintf("Stack %q started on %q", gf.Stack, hc.name),
			)
		},
	}
}
