// Command handoff validates task-result artifacts and records restart pointers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := runHandoff(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runHandoff(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("handoff", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	task := flags.String("task", "validate", "snapshot, run, validate, or record")
	repo := flags.String("repo", ".", "source repository")
	name := flags.String("file", "", "result JSON artifact")
	index := flags.String("index", "", "controller-owned scratch execution index")
	current := flags.Bool("current", true, "require matching current source; false is historical validation only")
	log := flags.String("log", "", "scratch output log for task=run")
	purpose := flags.String("purpose", "verification", "run evidence purpose: red, verification, baseline")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *task == "run" {
		if flags.NArg() == 0 || *log == "" || *name != "" || *index != "" {
			return errors.New("run requires log and command argv after --; no file/index")
		}
		return captureCommand(ctx, *repo, *log, *purpose, flags.Args(), output, diagnostics)
	}
	if flags.NArg() != 0 || *log != "" {
		return errors.New("use named flags")
	}
	if *task == "snapshot" {
		if *name != "" || *index != "" {
			return errors.New("snapshot does not accept file/index")
		}
		state, err := snapshot(ctx, *repo)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(state)
	}
	if (*task != "validate" && *task != "record") || *name == "" || (*task == "record" && *index == "") || (*task == "validate" && *index != "") {
		return errors.New("validate requires file; record requires file and index")
	}
	file, err := os.Open(*name)
	if err != nil {
		return err
	}
	result, decodeErr := decodeResult(file)
	closeErr := file.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		return err
	}
	if err := result.Validate(filepath.Dir(*name)); err != nil {
		return err
	}
	if *current {
		state, err := snapshot(ctx, *repo)
		if err != nil {
			return err
		}
		if state != result.Source {
			return errors.New("source changed since evidence capture; re-verify before accepting")
		}
	}
	if *task == "record" {
		if !*current {
			return errors.New("record requires current-source validation; historical evidence cannot advance the index")
		}
		if err := recordResult(*index, *name, result); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(output, "validated "+result.Task+" "+result.Role+" "+result.Status+" (artifact integrity only)")
	return err
}
