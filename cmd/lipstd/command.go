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
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
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

type CommandName string

const (
	CommandServe       CommandName = "serve"
	CommandCheckConfig CommandName = "check-config"
	CommandRoutes      CommandName = "routes"
	CommandInventory   CommandName = "inventory"
)

type CommandOptions struct {
	Name           CommandName
	ConfigPath     string
	StreamRecovery config.StreamRecoveryOverrides
	Output         io.Writer
	ErrorOut       io.Writer
}

type ParsedArgs struct {
	ConfigPath     string
	Name           CommandName
	StreamRecovery config.StreamRecoveryOverrides
}

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

func parseCLIPrefix(argv []string) (prefixArgs []string, name CommandName, tail []string) {
	i := 0
	for i < len(argv) {
		a := argv[i]
		if flagTakesValue(a) {
			prefixArgs = append(prefixArgs, a)
			i++
			if i < len(argv) && !hasInlineFlagValue(a) {
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

func flagTakesValue(a string) bool {
	switch a {
	case "-config", "--config", "-auto-resume", "--auto-resume", "-auto-resume-idle-timeout", "--auto-resume-idle-timeout", "-auto-resume-grace-period", "--auto-resume-grace-period":
		return true
	default:
		return hasInlineFlagValue(a)
	}
}

func hasInlineFlagValue(a string) bool {
	return len(a) > 0 && a[0] == '-' && containsEqual(a)
}

func containsEqual(s string) bool {
	for _, r := range s {
		if r == '=' {
			return true
		}
	}
	return false
}

func ParseArgs(argv []string, usageOut io.Writer) (configPath string, name CommandName, err error) {
	parsed, err := ParseArgsFull(argv, usageOut)
	if err != nil {
		return "", "", err
	}
	return parsed.ConfigPath, parsed.Name, nil
}

func ParseArgsFull(argv []string, usageOut io.Writer) (ParsedArgs, error) {
	if usageOut == nil {
		usageOut = io.Discard
	}
	prefixArgs, name, tail := parseCLIPrefix(argv)
	out := ParsedArgs{ConfigPath: "./config/config.yaml", Name: name}
	if err := parseCommandFlags("lipstd", prefixArgs, usageOut, &out); err != nil {
		return ParsedArgs{}, err
	}
	if len(tail) > 0 {
		if err := parseCommandFlags(string(name), tail, usageOut, &out); err != nil {
			return ParsedArgs{}, err
		}
	}
	return out, nil
}

func parseCommandFlags(name string, args []string, usageOut io.Writer, out *ParsedArgs) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(usageOut)
	var autoResume string
	var idleTimeout string
	var gracePeriod string
	fs.StringVar(&out.ConfigPath, "config", out.ConfigPath, "path to runtime config")
	fs.StringVar(&autoResume, "auto-resume", "", "enable stream auto-resume/recovery")
	fs.StringVar(&idleTimeout, "auto-resume-idle-timeout", "", "auto-resume idle timeout")
	fs.StringVar(&gracePeriod, "auto-resume-grace-period", "", "auto-resume grace period")
	fs.Usage = func() { printLipstdUsage(fs) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if extra := fs.Args(); len(extra) > 0 {
		return fmt.Errorf("unexpected arguments: %v", extra)
	}
	if autoResume != "" {
		v, err := parseBoolFlag("auto-resume", autoResume)
		if err != nil {
			return err
		}
		out.StreamRecovery.CLIEnabled = &v
	}
	if idleTimeout != "" {
		d, err := time.ParseDuration(idleTimeout)
		if err != nil || d <= 0 {
			return fmt.Errorf("auto-resume-idle-timeout: invalid positive duration %q", idleTimeout)
		}
		out.StreamRecovery.CLIIdleTimeout = d
	}
	if gracePeriod != "" {
		d, err := time.ParseDuration(gracePeriod)
		if err != nil || d <= 0 {
			return fmt.Errorf("auto-resume-grace-period: invalid positive duration %q", gracePeriod)
		}
		out.StreamRecovery.CLIGracePeriod = d
	}
	return nil
}

func parseBoolFlag(name, raw string) (bool, error) {
	switch raw {
	case "true", "1", "t", "TRUE", "True":
		return true, nil
	case "false", "0", "f", "FALSE", "False":
		return false, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean %q", name, raw)
	}
}

func runServeCommand(ctx context.Context, opts CommandOptions) int {
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath:              opts.ConfigPath,
		Mode:                    runtimebundle.BootstrapServe,
		Mandatory:               mandatoryStandardPlugins(),
		LogWriter:               opts.Output,
		StreamRecoveryOverrides: opts.StreamRecovery,
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
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{ConfigPath: opts.ConfigPath, Mode: runtimebundle.BootstrapInspect, Mandatory: mandatoryStandardPlugins(), LogWriter: io.Discard, StreamRecoveryOverrides: opts.StreamRecovery})
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "configuration invalid: %v\n", err)
		return 1
	}
	defer func() { deferBootstrapTracingShutdown(ctx, &res) }()
	_, _ = fmt.Fprintln(opts.Output, "configuration is valid")
	return 0
}

func runRoutesCommand(ctx context.Context, opts CommandOptions) int {
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{ConfigPath: opts.ConfigPath, Mode: runtimebundle.BootstrapInspect, Mandatory: mandatoryStandardPlugins(), LogWriter: io.Discard, StreamRecoveryOverrides: opts.StreamRecovery})
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
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{ConfigPath: opts.ConfigPath, Mode: runtimebundle.BootstrapInspect, Mandatory: mandatoryStandardPlugins(), LogWriter: io.Discard, StreamRecoveryOverrides: opts.StreamRecovery})
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
