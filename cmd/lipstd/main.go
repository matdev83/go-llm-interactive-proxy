package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	configPath, name, err := ParseArgs(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "lipstd: %v\n", err)
		os.Exit(2)
	}

	code := RunCommand(context.Background(), CommandOptions{
		Name:       name,
		ConfigPath: configPath,
		Output:     os.Stdout,
		ErrorOut:   os.Stderr,
	})
	os.Exit(code)
}
