package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

var version = "dev"

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		_, _ = fmt.Fprintf(stdout, "lipstd %s\n", version)
		return 0
	}

	parsed, err := ParseArgsFull(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "lipstd: %v\n", err)
		return 2
	}

	return RunCommand(context.Background(), CommandOptions{
		Name:           parsed.Name,
		ConfigPath:     parsed.ConfigPath,
		StreamRecovery: parsed.StreamRecovery,
		MultiUser:      parsed.MultiUser,
		Components:     parsed.Components,
		InstanceID:     parsed.InstanceID,
		Output:         stdout,
		ErrorOut:       stderr,
	})
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
