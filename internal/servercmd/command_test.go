package servercmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/portpilot/portpilot/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
)

func TestPasswordHashFromStdin(t *testing.T) {
	t.Parallel()
	command := NewCommand()
	var stdout, stderr bytes.Buffer
	command.SetIn(strings.NewReader("a password with spaces  \r\n"))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"password-hash", "--password-stdin", "--cost", "10"})
	if err := command.Execute(); err != nil {
		t.Fatalf("password-hash failed: %v", err)
	}
	hash := strings.TrimSpace(stdout.String())
	if err := bcrypt.CompareHashAndPassword(
		[]byte(hash), []byte("a password with spaces  "),
	); err != nil {
		t.Fatalf("generated hash does not verify: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("password-hash wrote unexpected stderr: %q", stderr.String())
	}
}

func TestPasswordHashRejectsInvalidInputWithoutOutput(t *testing.T) {
	t.Parallel()
	for name, arguments := range map[string][]string{
		"empty":    {"password-hash", "--password-stdin"},
		"too long": {"password-hash", "--password-stdin"},
		"cost":     {"password-hash", "--password-stdin", "--cost", "9"},
	} {
		t.Run(name, func(t *testing.T) {
			command := NewCommand()
			var stdout bytes.Buffer
			input := ""
			if name == "too long" {
				input = strings.Repeat("x", 73) + "\n"
			}
			command.SetIn(strings.NewReader(input))
			command.SetOut(&stdout)
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(arguments)
			err := command.Execute()
			if err == nil {
				t.Fatal("invalid password input succeeded")
			}
			if name == "cost" && !cli.IsUsageError(err) {
				t.Fatalf("invalid cost is not a usage error: %v", err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid input leaked output: %q", stdout.String())
			}
		})
	}
}

func TestRootRequiresSubcommand(t *testing.T) {
	t.Parallel()
	command := cli.NewClientCommand()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(nil)
	err := command.Execute()
	if err == nil || !cli.IsUsageError(err) {
		t.Fatalf("root command error = %v, want usage error", err)
	}
}

func TestRunCommandsDoNotExposeDebugFlag(t *testing.T) {
	t.Parallel()
	for name, root := range map[string]*cobra.Command{
		"server": NewCommand(),
		"client": cli.NewClientCommand(),
	} {
		run, _, err := root.Find([]string{"run"})
		if err != nil {
			t.Fatalf("%s run command: %v", name, err)
		}
		if run.Flags().Lookup("debug") != nil {
			t.Fatalf("%s run command exposes --debug", name)
		}
	}
}
