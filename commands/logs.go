package commands

import (
	"fmt"

	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"github.com/spf13/cobra"
)

func newLogsCmd(gf *GlobalFlags) *cobra.Command {
	var follow bool
	var tail string

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View container logs",
		Example: `  marina logs -H myhost -c mycontainer
  marina logs -H myhost -c mycontainer -f
  marina logs -H myhost -c mycontainer --tail 100`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if gf.Container == "" {
				return fmt.Errorf("container is required: use -c <container>")
			}

			hc, err := resolveHost(gf)
			if err != nil {
				return err
			}

			logCmd := "docker logs"
			if tail != "" {
				logCmd += " --tail " + tail
			}
			if follow {
				logCmd += " -f"
			}
			logCmd += " " + gf.Container

			return internalssh.Stream(cmd.Context(), hc.sshCfg, logCmd, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().StringVar(&tail, "tail", "", "Number of lines to show from the end")
	return cmd
}
