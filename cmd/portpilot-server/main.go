package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/server"
	"github.com/portpilot/portpilot/internal/version"
)

func main() {
	os.Exit(run())
}

func run() int {
	command := "run"
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		command = os.Args[1]
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	}
	switch command {
	case "version":
		fmt.Println(version.String())
		return 0
	case "validate-config":
		flags := flag.NewFlagSet("validate-config", flag.ContinueOnError)
		path := flags.String("config", "server.json", "server configuration file")
		if err := flags.Parse(os.Args[1:]); err != nil {
			return 2
		}
		serverConfig, err := config.LoadServer(*path)
		if err == nil {
			_, err = config.LoadClientFile(serverConfig.ClientConfigFile)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("configuration valid")
		return 0
	case "run":
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		return 2
	}

	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	path := flags.String("config", "server.json", "server configuration file")
	debug := flags.Bool("debug", false, "enable debug logging")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return 2
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := server.DefaultLogger(level)
	app, err := server.New(*path, logger)
	if err != nil {
		logger.Error("server initialization failed", "error", err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := app.Run(ctx); err != nil {
		logger.Error("server stopped", "error", err)
		return 1
	}
	return 0
}
