package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/portpilot/portpilot/internal/client"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/spf13/cobra"
)

func NewClientCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "portpilot-client",
		Short: "PortPilot controlled client",
		Args:  NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Help()
			return usageError(errors.New("a subcommand is required"))
		},
	}
	ConfigureRoot(root)
	root.AddCommand(newClientRunCommand(), newClientValidateCommand())
	return root
}

func newClientRunCommand() *cobra.Command {
	var path string
	var debug bool
	command := &cobra.Command{
		Use:   "run",
		Short: "Run the PortPilot client",
		Args:  NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			level := slog.LevelInfo
			if debug {
				level = slog.LevelDebug
			}
			logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
			app, err := client.New(path, logger)
			if err != nil {
				return fmt.Errorf("initialize client: %w", err)
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			if err := app.Run(ctx); err != nil {
				return fmt.Errorf("run client: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&path, "config", "client.json", "client configuration file")
	command.Flags().BoolVar(&debug, "debug", false, "enable debug logging")
	return command
}

func newClientValidateCommand() *cobra.Command {
	var path string
	command := &cobra.Command{
		Use:   "validate-config",
		Short: "Validate client configuration",
		Args:  NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := config.LoadClient(path); err != nil {
				return fmt.Errorf("validate configuration: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
			return nil
		},
	}
	command.Flags().StringVar(&path, "config", "client.json", "client configuration file")
	return command
}
