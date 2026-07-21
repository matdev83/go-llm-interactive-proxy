package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/dbmigrate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
)

func printLipstdUsage(fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(fs.Output(),
		"Usage: lipstd [--config path] [serve|check-config|routes|inventory|migrate]\n\n",
	)
	fs.PrintDefaults()
}

type CommandName string

const (
	CommandServe       CommandName = "serve"
	CommandCheckConfig CommandName = "check-config"
	CommandRoutes      CommandName = "routes"
	CommandInventory   CommandName = "inventory"
	CommandMigrate     CommandName = "migrate"
)

type CommandOptions struct {
	Name           CommandName
	ConfigPath     string
	StreamRecovery config.StreamRecoveryOverrides
	MultiUser      *bool
	Output         io.Writer
	ErrorOut       io.Writer
	Components     string
}

type ParsedArgs struct {
	ConfigPath     string
	Name           CommandName
	StreamRecovery config.StreamRecoveryOverrides
	MultiUser      *bool
	Components     string
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
	case CommandMigrate:
		return runMigrateCommand(ctx, opts)
	default:
		_, _ = fmt.Fprintf(opts.ErrorOut, "lipstd: unknown command %q\n", opts.Name)
		return 2
	}
}

const migrationPostgresDSNEnv = "LIP_MIGRATION_POSTGRES_DSN"

// runPostgresMigrate applies and verifies selected components. Tests may override.
var runPostgresMigrate = dbmigrate.PostgresComponents

func runMigrateCommand(ctx context.Context, opts CommandOptions) int {
	components, err := dbmigrate.ParseComponents(opts.Components)
	if err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "lipstd migrate: %v\n", err)
		return 2
	}
	dsn := strings.TrimSpace(os.Getenv(migrationPostgresDSNEnv))
	if dsn == "" {
		_, _ = fmt.Fprintf(opts.ErrorOut, "lipstd migrate: set %s\n", migrationPostgresDSNEnv)
		return 1
	}
	child, cancel := context.WithTimeout(ctx, db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	if err := runPostgresMigrate(child, dsn, components); err != nil {
		_, _ = fmt.Fprintf(opts.ErrorOut, "lipstd migrate: %v\n", err)
		return 1
	}
	return 0
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
	case string(CommandMigrate):
		return CommandMigrate, nil
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
		case CommandServe, CommandCheckConfig, CommandRoutes, CommandInventory, CommandMigrate:
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

// hasInlineFlagValue reports whether a is a flag-shaped token that has its value
// embedded after an '=' separator (e.g. "--config=./x.yaml"). The leading '-'
// guard prevents treating positional arguments that contain '=' (e.g.
// "something=other") as flag-with-embedded-value tokens.
func hasInlineFlagValue(a string) bool {
	return len(a) > 0 && a[0] == '-' && strings.Contains(a, "=")
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
	fs.StringVar(&out.Components, "components", out.Components, "comma-separated migration components")
	var multiUser bool
	fs.BoolVar(&multiUser, "multi-user", false, "opt in to access.mode multi_user for serve")
	fs.Usage = func() { printLipstdUsage(fs) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if extra := fs.Args(); len(extra) > 0 {
		return fmt.Errorf("unexpected arguments: %v", extra)
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "multi-user" {
			v := multiUser
			out.MultiUser = &v
		}
	})
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
	if err := validateServeMultiUserGate(ctx, opts.ConfigPath, opts.MultiUser, opts.StreamRecovery); err != nil {
		if errors.Is(err, accessmode.ErrMultiUserFlagRequired) || errors.Is(err, accessmode.ErrMultiUserFlagInconsistent) {
			_, _ = fmt.Fprintf(opts.ErrorOut, "lipstd: %v\n", err)
			return 2
		}
		_, _ = fmt.Fprintf(opts.ErrorOut, "bootstrap failed: %v\n", err)
		return 1
	}
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath:              opts.ConfigPath,
		Mode:                    runtimebundle.BootstrapServe,
		Mandatory:               mandatoryStandardPlugins(),
		LogWriter:               opts.Output,
		StreamRecoveryOverrides: opts.StreamRecovery,
		HandlerComposer:         stdhttp.ComposeRequestPlane,
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
	// INT/TERM shut down the server; SIGHUP is owned by the reload adapter when a
	// sink is wired (task 5.2). Sink remains nil until management/coordinator composition.
	sigCtx, stop := startServeSignalHandling(ctx, nil)
	defer stop()
	if err := stdhttp.RunWithGenerationHost(sigCtx, stdhttp.GenerationHostInput{
		Config:  res.Config,
		Log:     res.Logger,
		Manager: res.GenerationManager,
		Process: res.ProcessServices,
	}); err != nil {
		res.Logger.ErrorContext(sigCtx, "server stopped", "error", err)
		return 1
	}
	return 0
}

// validateServeMultiUserGate enforces the --multi-user CLI flag consistency
// against access.mode for serve mode. It is a CLI-layer concern: it loads the
// config through the shared strict effective pipeline, resolves the effective
// access mode, and applies [accessmode.ValidateServeModeGate] before heavy
// runtime assembly in [runtimebundle.BuildBootstrap]. Runtime posture/security
// validation (backend access scopes, credential modes) stays in runtimebundle.Build.
func validateServeMultiUserGate(ctx context.Context, configPath string, multiUserFlag *bool, streamOverrides config.StreamRecoveryOverrides) error {
	eff, err := runtimebundle.LoadBootstrapEffective(ctx, configPath, streamOverrides)
	if err != nil {
		return err
	}
	mode, err := eff.Config.EffectiveAccessMode()
	if err != nil {
		return fmt.Errorf("bootstrap access/auth: %w", err)
	}
	return accessmode.ValidateServeModeGate(mode, multiUserFlag)
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
