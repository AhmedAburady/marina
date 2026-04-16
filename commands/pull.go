package commands

import (
	"fmt"

	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"github.com/spf13/cobra"
)

func newPullCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Pull latest images for a compose stack",
		Example: `  marina pull -H myhost -s mystack`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if gf.Stack == "" {
				return fmt.Errorf("stack is required: use -s <stack>")
			}

			hc, err := resolveHost(gf)
			if err != nil {
				return err
			}

			dir, err := findStackDir(cmd.Context(), hc, gf.Stack)
			if err != nil {
				return err
			}

			pullCmd := fmt.Sprintf("cd %s && docker compose pull", dir)
			if err := internalssh.Stream(cmd.Context(), hc.sshCfg, pullCmd, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return err
			}

			cmd.Printf("Pulled latest images for stack %q on %q\n", gf.Stack, hc.name)
			return nil
		},
	}
}
