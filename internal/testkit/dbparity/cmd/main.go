package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
)

func main() {
	var (
		modeFlag      string
		componentFlag string
		onlyFlag      string
		formatFlag    string
		jsonFlag      bool
		testFlags     string
	)

	flag.StringVar(&modeFlag, "mode", "", "Runner mode (list, sqlite, postgres-direct, all)")
	flag.StringVar(&componentFlag, "component", "", "Filter to a specific component ID")
	flag.StringVar(&onlyFlag, "only", "", "Alias for -component")
	flag.StringVar(&formatFlag, "format", "text", "List output format: text or json (list mode only)")
	flag.StringVar(&formatFlag, "list-format", "text", "Alias for -format")
	flag.BoolVar(&jsonFlag, "json", false, "Output list mode in JSON format")
	flag.StringVar(&testFlags, "flags", "", "Extra flags passed through to go test (overrides GO_TEST_FLAGS env if set)")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: dbparity [flags] [mode]\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Modes:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  list             Show catalog component and test package inventory\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  sqlite           Execute canonical SQLite parity tests\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  postgres-direct  Execute canonical PostgreSQL direct parity tests (fail-closed)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  all              Execute SQLite followed by PostgreSQL direct parity tests (default)\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// Mode and trailing flag resolution: allow flags before or after mode arg
	modeStr := strings.TrimSpace(modeFlag)
	for i := 0; i < flag.NArg(); i++ {
		arg := strings.TrimSpace(flag.Arg(i))
		switch {
		case arg == "-json" || arg == "--json":
			jsonFlag = true
		case strings.HasPrefix(arg, "-format="):
			formatFlag = strings.TrimPrefix(arg, "-format=")
		case strings.HasPrefix(arg, "--format="):
			formatFlag = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "-list-format="):
			formatFlag = strings.TrimPrefix(arg, "-list-format=")
		case strings.HasPrefix(arg, "--list-format="):
			formatFlag = strings.TrimPrefix(arg, "--list-format=")
		case strings.HasPrefix(arg, "-component="):
			componentFlag = strings.TrimPrefix(arg, "-component=")
		case strings.HasPrefix(arg, "--component="):
			componentFlag = strings.TrimPrefix(arg, "--component=")
		case strings.HasPrefix(arg, "-only="):
			onlyFlag = strings.TrimPrefix(arg, "-only=")
		case strings.HasPrefix(arg, "--only="):
			onlyFlag = strings.TrimPrefix(arg, "--only=")
		case !strings.HasPrefix(arg, "-") && modeStr == "":
			modeStr = arg
		}
	}
	if modeStr == "" {
		modeStr = string(dbparity.ModeAll)
	}

	mode, err := dbparity.ParseRunnerMode(modeStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", dbparity.RedactDSN(err.Error()))
		flag.Usage()
		os.Exit(2)
	}

	// Component filter resolution
	component := strings.TrimSpace(componentFlag)
	if component == "" {
		component = strings.TrimSpace(onlyFlag)
	}

	// Test flags resolution
	var goTestFlags []string
	if strings.TrimSpace(testFlags) != "" {
		goTestFlags = strings.Fields(testFlags)
	} else if envFlags := strings.TrimSpace(os.Getenv("GO_TEST_FLAGS")); envFlags != "" {
		goTestFlags = strings.Fields(envFlags)
	}

	cat := dbparity.DefaultCatalog()

	// Handle list mode with optional JSON format
	if mode == dbparity.ModeList {
		if jsonFlag || strings.EqualFold(formatFlag, "json") {
			jsonOut, jsonErr := dbparity.FormatListJSON(cat)
			if jsonErr != nil {
				fmt.Fprintf(os.Stderr, "Error formatting JSON: %s\n", dbparity.RedactDSN(jsonErr.Error()))
				os.Exit(1)
			}
			fmt.Println(jsonOut)
			return
		}
		fmt.Println(dbparity.FormatList(cat))
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	opts := dbparity.PlanOptions{
		Catalog:     cat,
		GoTestFlags: goTestFlags,
		ComponentID: component,
		BaseEnv:     os.Environ(),
	}

	if runErr := dbparity.Run(ctx, mode, opts, os.Stdout, os.Stderr); runErr != nil {
		var stepErr *dbparity.RunStepError
		if errors.As(runErr, &stepErr) {
			exitCode := dbparity.MapExitStatus(stepErr)
			fmt.Fprintf(os.Stderr, "\ndbparity: test failed for component %q package %q (backend: %s, exit code: %d)\n",
				stepErr.Component, stepErr.Package, stepErr.Backend, exitCode)
			os.Exit(exitCode)
		}

		exitCode := dbparity.MapExitStatus(runErr)
		fmt.Fprintf(os.Stderr, "\nError: %s\n", dbparity.RedactDSN(runErr.Error()))
		os.Exit(exitCode)
	}
}
