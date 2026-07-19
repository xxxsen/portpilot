package main

import (
	"fmt"
	"os"

	"github.com/portpilot/portpilot/internal/cli"
	"github.com/portpilot/portpilot/internal/servercmd"
)

func main() {
	command := servercmd.NewCommand()
	if err := command.Execute(); err != nil {
		_, _ = fmt.Fprintln(command.ErrOrStderr(), err)
		if cli.IsUsageError(err) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
