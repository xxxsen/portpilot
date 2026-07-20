package cli

import (
	"errors"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/portpilot/portpilot/internal/client"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/logging"
	"github.com/spf13/cobra"
	"github.com/xxxsen/common/logutil"
	"go.uber.org/zap"
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
	command := &cobra.Command{
		Use:   "run",
		Short: "Run the PortPilot client",
		Args:  NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadClient(path)
			if err != nil {
				return fmt.Errorf("load client configuration: %w", err)
			}
			baseLogger, err := logging.Init(cfg.LogConfig)
			if err != nil {
				return fmt.Errorf("initialize logging: %w", err)
			}
			defer logging.Sync(baseLogger)
			runtimeLogger := logutil.GetLogger(cmd.Context())
			runtimeLogger.Info(
				"logging initialized",
				zap.String("component", "client"),
				zap.String("level", cfg.LogConfig.Level),
				zap.Bool("console", cfg.LogConfig.Console),
				zap.Bool("file_enabled", cfg.LogConfig.File != ""),
			)
			app, err := client.NewFromConfig(cfg, runtimeLogger)
			if err != nil {
				return fmt.Errorf("initialize client: %w", err)
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			if err := app.Run(ctx); err != nil {
				runtimeLogger.Error("client stopped with error", zap.Error(err))
				return fmt.Errorf("run client: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&path, "config", "client.json", "client configuration file")
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
