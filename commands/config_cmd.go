package commands

import (
	"fmt"
	"strings"

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
		Example: `  marina config set username ahmed`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}

			switch key {
			case "username":
				cfg.Settings.Username = value
			case "ssh_key":
				cfg.Settings.SSHKey = value
			default:
				return fmt.Errorf("unknown config key %q (supported: username, ssh_key)", key)
			}

			if err := config.Save(cfg, gf.Config); err != nil {
				return err
			}

			cmd.Printf("Set %s = %q\n", key, value)
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
