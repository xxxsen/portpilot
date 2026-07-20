package servercmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/portpilot/portpilot/internal/cli"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/logging"
	"github.com/portpilot/portpilot/internal/server"
	"github.com/spf13/cobra"
	"github.com/xxxsen/common/logutil"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "portpilot-server",
		Short: "PortPilot reverse proxy server",
		Args:  cli.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Help()
			return cli.NewUsageError(errors.New("a subcommand is required"))
		},
	}
	cli.ConfigureRoot(root)
	root.AddCommand(newServerRunCommand(), newServerValidateCommand(), newPasswordHashCommand())
	return root
}

func newServerRunCommand() *cobra.Command {
	var path string
	command := &cobra.Command{
		Use:   "run",
		Short: "Run the PortPilot server",
		Args:  cli.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadServer(path)
			if err != nil {
				return fmt.Errorf("load server configuration: %w", err)
			}
			baseLogger, err := logging.Init(cfg.LogConfig)
			if err != nil {
				return fmt.Errorf("initialize logging: %w", err)
			}
			defer logging.Sync(baseLogger)
			runtimeLogger := logutil.GetLogger(cmd.Context())
			runtimeLogger.Info(
				"logging initialized",
				zap.String("component", "server"),
				zap.String("level", cfg.LogConfig.Level),
				zap.Bool("console", cfg.LogConfig.Console),
				zap.Bool("file_enabled", cfg.LogConfig.File != ""),
			)
			app, err := server.NewFromConfig(cfg, runtimeLogger)
			if err != nil {
				return fmt.Errorf("initialize server: %w", err)
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			if err := app.Run(ctx); err != nil {
				runtimeLogger.Error("server stopped with error", zap.Error(err))
				return fmt.Errorf("run server: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&path, "config", "server.json", "server configuration file")
	return command
}

func newServerValidateCommand() *cobra.Command {
	var path string
	command := &cobra.Command{
		Use:   "validate-config",
		Short: "Validate server and proxy configuration",
		Args:  cli.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			serverConfig, err := config.LoadServer(path)
			if err == nil {
				_, err = config.LoadClientFile(serverConfig.ClientConfigFile)
			}
			if err != nil {
				return fmt.Errorf("validate configuration: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
			return nil
		},
	}
	command.Flags().StringVar(&path, "config", "server.json", "server configuration file")
	return command
}

func newPasswordHashCommand() *cobra.Command {
	var cost int
	var passwordStdin bool
	command := &cobra.Command{
		Use:   "password-hash",
		Short: "Generate a bcrypt administrator password hash",
		Args:  cli.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if cost < 10 || cost > 16 {
				return cli.NewUsageError(errors.New("cost must be between 10 and 16"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			var password []byte
			var err error
			if passwordStdin {
				password, err = readPasswordLine(cmd.InOrStdin())
			} else {
				password, err = readConfirmedPassword(cmd.ErrOrStderr(), cmd.InOrStdin())
			}
			if err != nil {
				return fmt.Errorf("read password input: %w", err)
			}
			if len(password) == 0 {
				return errors.New("password must not be empty")
			}
			if len(password) > 72 {
				return errors.New("password exceeds bcrypt 72-byte limit")
			}
			hash, err := bcrypt.GenerateFromPassword(password, cost)
			if err != nil {
				return fmt.Errorf("generate password hash: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(hash))
			if err != nil {
				return fmt.Errorf("write password hash: %w", err)
			}
			return nil
		},
	}
	command.Flags().IntVar(&cost, "cost", 12, "bcrypt cost (10-16)")
	command.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read one password line from stdin")
	return command
}

func readPasswordLine(input io.Reader) ([]byte, error) {
	reader := bufio.NewReaderSize(input, 74)
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, errors.New("password exceeds bcrypt 72-byte limit")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read password: %w", err)
	}
	line = bytesWithoutLineEnding(line)
	if len(line) > 72 {
		return nil, errors.New("password exceeds bcrypt 72-byte limit")
	}
	return line, nil
}

func bytesWithoutLineEnding(value []byte) []byte {
	value = []byte(strings.TrimSuffix(string(value), "\n"))
	value = []byte(strings.TrimSuffix(string(value), "\r"))
	return value
}

func readConfirmedPassword(output io.Writer, input io.Reader) ([]byte, error) {
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return nil, errors.New("interactive password input requires a terminal; use --password-stdin")
	}
	_, _ = fmt.Fprint(output, "Password: ")
	first, err := term.ReadPassword(int(file.Fd()))
	_, _ = fmt.Fprintln(output)
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}
	_, _ = fmt.Fprint(output, "Confirm password: ")
	second, err := term.ReadPassword(int(file.Fd()))
	_, _ = fmt.Fprintln(output)
	if err != nil {
		return nil, fmt.Errorf("read password confirmation: %w", err)
	}
	if string(first) != string(second) {
		return nil, errors.New("passwords do not match")
	}
	return first, nil
}
