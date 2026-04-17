package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRestartCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart a container or compose stack",
		Example: `  marina restart -H myhost -s mystack
  marina restart -H myhost -c mycontainer`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if gf.Stack == "" && gf.Container == "" {
				return fmt.Errorf("specify -s <stack> or -c <container>")
			}
			hc, err := resolveHost(gf)
			if err != nil {
				return err
			}
			if gf.Container != "" {
				return execWithSpinner(cmd.Context(), cmd.OutOrStdout(), hc,
					fmt.Sprintf("Restarting container %s on %s...", gf.Container, hc.name),
					"docker restart "+gf.Container,
					fmt.Sprintf("Restarted container %q on %q", gf.Container, hc.name),
				)
			}
			return execStackWithSpinner(cmd.Context(), cmd.OutOrStdout(), hc, gf.Stack,
				fmt.Sprintf("Restarting stack %s on %s...", gf.Stack, hc.name),
				"cd %s && docker compose restart",
				fmt.Sprintf("Restarted stack %q on %q", gf.Stack, hc.name),
			)
		},
	}
}
