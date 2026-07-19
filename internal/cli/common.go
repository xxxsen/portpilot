package cli

import (
	"fmt"

	"github.com/portpilot/portpilot/internal/version"
	"github.com/spf13/cobra"
)

func NoArgs(command *cobra.Command, args []string) error {
	if err := cobra.NoArgs(command, args); err != nil {
		return usageError(err)
	}
	return nil
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(count)(command, args); err != nil {
			return usageError(err)
		}
		return nil
	}
}

func ConfigureRoot(command *cobra.Command) {
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError(err)
	})
	command.CompletionOptions.DisableDefaultCmd = true
	command.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print build version",
			Args:  NoArgs,
			Run: func(cmd *cobra.Command, _ []string) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), version.String())
			},
		},
		completionCommand(command),
	)
}

func completionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion bash|zsh|fish|powershell",
		Short:     "Generate shell completion",
		Args:      exactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(cmd.OutOrStdout())
			default:
				return usageError(fmt.Errorf("unsupported shell %q", args[0]))
			}
		},
	}
}
