package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/tools/devcheck/internal/testscope"
)

func runContractCheck(root, base string, opts testPlanOptions, output, diagnostics io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	planning, cancel := context.WithTimeout(ctx, 2*time.Minute)
	plan, err := testscope.BuildContracts(planning, root, base)
	cancel()
	if err != nil {
		return err
	}
	if err := json.NewEncoder(diagnostics).Encode(plan); err != nil {
		return err
	}
	if opts.dry {
		_, err := fmt.Fprintln(diagnostics, "plan-only: no contracts or lint executed")
		return err
	}
	if len(plan.Tests) != 0 {
		// A removed/renamed test must not turn an exact -run filter into a
		// successful no-op. Ask the test binary, not a source-text scanner.
		cmd := exec.CommandContext(ctx, "go", "test", "-mod=readonly", "-trimpath", "-list", contractPattern(plan.Tests), "./internal/archtest")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		cmd.Stderr = diagnostics
		listed, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("list selected contracts: %w", err)
		}
		if err := validateListedContracts(plan.Tests, string(listed)); err != nil {
			return err
		}
	}
	for n := 1; n <= opts.repeat; n++ {
		if len(plan.Tests) != 0 {
			command, err := commandFor("test", "./internal/archtest", opts.jobs, opts.fresh)
			if err != nil {
				return err
			}
			command = slices.Insert(command, len(command)-1, "-trimpath", "-run", contractPattern(plan.Tests))
			if err := runContractCommandRecorded(ctx, root, command, true, output, diagnostics, opts.recorder); err != nil {
				return err
			}
		}
		for _, module := range plan.Lint {
			dir, err := moduleDirectory(root, module.Directory)
			if err != nil {
				return err
			}
			command, err := commandFor("lint", strings.Join(module.Packages, " "), opts.jobs, false)
			if err != nil {
				return err
			}
			if err := runContractCommandRecorded(ctx, dir, command, false, output, diagnostics, opts.recorder); err != nil {
				return err
			}
		}
	}
	_, err = fmt.Fprintln(diagnostics, "Local contract/lint feedback only; comprehensive delivery gates remain required.")
	return err
}

func runContractCommand(ctx context.Context, dir string, command []string, test bool, output, diagnostics io.Writer) error {
	return runContractCommandRecorded(ctx, dir, command, test, output, diagnostics, nil)
}

func runContractCommandRecorded(ctx context.Context, dir string, command []string, test bool, output, diagnostics io.Writer, recorder *evidenceRecorder) error {
	if _, err := fmt.Fprintf(diagnostics, "directory=%q command=%q\n", dir, command); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.Stderr = diagnostics
	step := recorder.start("contract-check", command, dir, output)
	stats, err := execute(cmd, test, step.output)
	recorder.finishRun(step, stepStats(test, stats), err)
	return err
}

func contractPattern(tests []string) string {
	quoted := make([]string, len(tests))
	for i, name := range tests {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

func validateListedContracts(tests []string, listed string) error {
	names := strings.Fields(listed)
	for _, name := range tests {
		if !slices.Contains(names, name) {
			return fmt.Errorf("selected contract %s missing; update the contract selector before continuing", name)
		}
	}
	return nil
}
