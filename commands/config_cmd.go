package commands

import (
	"fmt"
	"strings"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd(gf *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage marina configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolvedConfigPath(gf.Config)
			if err != nil {
				return err
			}
			cmd.Printf("Config file: %s\n", path)
			cmd.Println("Subcommands: path, validate, set")
			return nil
		},
	}

	cmd.AddCommand(
		newConfigPathCmd(gf),
		newConfigValidateCmd(gf),
		newConfigSetCmd(gf),
	)

	return cmd
}

func newConfigPathCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolvedConfigPath(gf.Config)
			if err != nil {
				return err
			}
			cmd.Println(path)
			return nil
		},
	}
}

func newConfigValidateCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the config file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolvedConfigPath(gf.Config)
			if err != nil {
				return err
			}

			cfg, err := config.Load(gf.Config)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			errs := config.Validate(cfg)
			if len(errs) == 0 {
				cmd.Printf("Config OK (%s) — %d host(s) configured\n", path, len(cfg.Hosts))
				return nil
			}

			cmd.Printf("Config errors in %s:\n", path)
			for _, e := range errs {
				cmd.Printf("  • %s\n", e)
			}
			return fmt.Errorf("%s", strings.Join(errs, "; "))
		},
	}
}

func newConfigSetCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Long: `Set a global config value.

Available keys:
  username             Global default SSH username for all hosts
  ssh_key              Global default SSH key path (tilde and $VAR expanded on load)
  prune_after_update   Auto-prune after update: true or false
  gotify.url           Gotify server URL
  gotify.token         Gotify app token (plaintext; prefer gotify.token_env)
  gotify.priority      Gotify notification priority (integer)`,
		Example: `  marina config set username myuser
  marina config set ssh_key ~/.ssh/id_ed25519
  marina config set prune_after_update true
  marina config set gotify.url https://notify.example.com
  marina config set gotify.priority 5`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}

			if err := actions.SetConfigKey(cfg, key, value); err != nil {
				return err
			}

			if err := config.Save(cfg, gf.Config); err != nil {
				return err
			}

			cmd.Printf("%s has been set to %q\n", key, value)
			return nil
		},
	}
}

func resolvedConfigPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return config.DefaultPath()
}
