package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
)

func printLipstdUsage(fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(fs.Output(),
		"Usage: lipstd [--config path] "+
			"[serve|check-config|routes|inventory] [--config path]\n\n",
	)
	fs.PrintDefaults()
}

// CommandName is a lipstd subcommand (first positional token matching a known name, or default serve).
type CommandName string

const (
	CommandServe       CommandName = "serve"
	CommandCheckConfig CommandName = "check-config"
	CommandRoutes      CommandName = "routes"
	CommandInventory   CommandName = "inventory"
)

// CommandOptions configures [RunCommand] for the standard distribution binary.
type CommandOptions struct {
	Name       CommandName
	ConfigPath string
	Output     io.Writer
	ErrorOut   io.Writer
}

// RunCommand executes one lipstd command and returns a process exit code.
func RunCommand(ctx context.Context, opts CommandOptions) int {
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
	if opts.ErrorOut == nil {
		opts.ErrorOut = os.Stderr
	}
	if ctx == nil {
		_, _ = fmt.Fprintln(opts.ErrorOut, "lipstd: nil context")
		return 2
	}
	switch opts.Name {
	case CommandServe:
		return runServeCommand(ctx, opts)
	case CommandCheckConfig:
		return runCheckConfigCommand(ctx, opts)
	case CommandRoutes:
		return runRoutesCommand(ctx, opts)
	case CommandInventory:
		return runInventoryCommand(ctx, opts)
	default:
		_, _ = fmt.Fprintf(opts.ErrorOut, "lipstd: unknown command %q\n", opts.Name)
		return 2
	}
}

func parseCommandName(args []string) (CommandName, error) {
	if len(args) == 0 {
		return CommandServe, nil
	}
	switch args[0] {
	case string(CommandServe):
		return CommandServe, nil
	case string(CommandCheckConfig):
		return CommandCheckConfig, nil
	case string(CommandRoutes):
		return CommandRoutes, nil
	case string(CommandInventory):
		return CommandInventory, nil
	default:
		return "", fmt.Errorf("unknown command %q", args[0])
	}
}

// parseCLIPrefix splits argv (typically os.Args[1:]) into tokens before the first recognized
// subcommand, the subcommand (default [CommandServe] when none appears), and tokens after it.
//
// A token that equals a subcommand name is not treated as the delimiter when it is the value of
// -config or --config (e.g. lipstd --config routes check-config uses config file "routes").
func parseCLIPrefix(argv []string) (prefixArgs []string, name CommandName, tail []string) {
	i := 0
	for i < len(argv) {
		a := argv[i]
		if a == "-config" || a == "--config" {
			prefixArgs = append(prefixArgs, a)
			i++
			if i < len(argv) {
				prefixArgs = append(prefixArgs, argv[i])
				i++
			}
			continue
		}
		switch CommandName(a) {
		case CommandServe, CommandCheckConfig, CommandRoutes, CommandInventory:
			return prefixArgs, CommandName(a), argv[i+1:]
		default:
			prefixArgs = append(prefixArgs, a)
			i++
		}
	}
	return prefixArgs, CommandServe, []string{}
}

// ParseArgs extracts -config and the subcommand from argv (excluding the program name).
// Global flags may appear before the subcommand; the same flags may repeat after the subcommand,
// in which case the latter value wins (e.g. lipstd --config a serve --config b uses b).
func ParseArgs(argv []string, usageOut io.Writer) (configPath string, name CommandName, err error) {
	if usageOut == nil {
		usageOut = io.Discard
	}
	prefixArgs, name, tail := parseCLIPrefix(argv)

	fs := flag.NewFlagSet("lipstd", flag.ContinueOnError)
	fs.SetOutput(usageOut)
	defaultCfg := "./config/config.yaml"
	fs.StringVar(&configPath, "config", defaultCfg, "path to runtime config")
	fs.Usage = func() { printLipstdUsage(fs) }
	if err := fs.Parse(prefixArgs); err != nil {
		return "", "", err
	}
	if extra := fs.Args(); len(extra) > 0 {
		return "", "", fmt.Errorf("unexpected arguments before subcommand: %v", extra)
	}

	if len(tail) > 0 {
		fs2 := flag.NewFlagSet(string(name), flag.ContinueOnError)
		fs2.SetOutput(usageOut)
		fs2.StringVar(&configPath, "config", configPath, "path to runtime config")
		fs2.Usage = func() { printLipstdUsage(fs2) }
		if err := fs2.Parse(tail); err != nil {
			return "", "", err
		}
		if extra := fs2.Args(); len(extra) > 0 {
			return "", "", fmt.Errorf("unexpected arguments: %v", extra)
		}
	}
	return configPath, name, nil
}

// runServeCommand wires bootstrap and blocks in stdhttp until interrupt. Full happy-path serve is
// not exercised in the default unit suite (signal-bound); early bootstrap failures are covered by
// [TestRunCommand_serve_bootstrapFailsOnMissingConfig], and HTTP wiring by stdhttp/dogfood tests.
func runServeCommand(ctx context.Context, opts CommandOptions) int {
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: opts.ConfigPath,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  mandatoryStandardPlugins(),
		LogWriter:  opts.Output,
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	defer func() { deferBootstrapTracingShutdown(ctx, &res) }()

	if err := logBootstrapAccessAuth(ctx, res.Logger, res.Config); err != nil {
		res.Logger.ErrorContext(ctx, "lipstd: bootstrap access/auth", "error", err)
		return 1
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := stdhttp.RunWithRuntime(sigCtx, res.Config, res.App, res.Logger, res.Built); err != nil {
		res.Logger.ErrorContext(sigCtx, "server stopped", "error", err)
		return 1
	}
	return 0
}

func runCheckConfigCommand(ctx context.Context, opts CommandOptions) int {
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: opts.ConfigPath,
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  mandatoryStandardPlugins(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "configuration invalid: %v\n", err)
		return 1
	}
	defer func() { deferBootstrapTracingShutdown(ctx, &res) }()
	_, _ = fmt.Fprintln(opts.Output, "configuration is valid")
	return 0
}

func runRoutesCommand(ctx context.Context, opts CommandOptions) int {
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: opts.ConfigPath,
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  mandatoryStandardPlugins(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	defer func() { deferBootstrapTracingShutdown(ctx, &res) }()
	snap, err := runtimebundle.RoutesSnapshotFrom(res.Config, res.Registry)
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "routes: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(opts.Output)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "routes: encode: %v\n", err)
		return 1
	}
	return 0
}

func runInventoryCommand(ctx context.Context, opts CommandOptions) int {
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: opts.ConfigPath,
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  mandatoryStandardPlugins(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	defer func() { deferBootstrapTracingShutdown(ctx, &res) }()
	snap, err := runtimebundle.InventorySnapshotForOperator(ctx, res.Config, res.Registry, res.Registrations)
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "inventory: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(opts.Output)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "inventory: encode: %v\n", err)
		return 1
	}
	return 0
}
