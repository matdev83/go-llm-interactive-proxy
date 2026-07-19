package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/comparison"
)

func main() {
	format := flag.String("format", "markdown", "markdown|json")
	fixture := flag.String("fixture", "", "optional path to comparison input JSON; empty uses synthetic fixture")
	outPath := flag.String("out", "", "optional output path; empty writes stdout (temp file used only when -out=temp)")
	flag.Parse()

	doc, err := loadDoc(*fixture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "comparison-report: %v\n", err)
		os.Exit(2)
	}
	rep, err := comparison.BuildReport(doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "comparison-report: %v\n", err)
		os.Exit(2)
	}

	w := os.Stdout
	cleanup := func() {}
	switch strings.ToLower(strings.TrimSpace(*outPath)) {
	case "", "-":
		// stdout
	case "temp":
		f, err := os.CreateTemp("", "cursor-sdk-comparison-*.md")
		if err != nil {
			fmt.Fprintf(os.Stderr, "comparison-report: %v\n", err)
			os.Exit(2)
		}
		w = f
		cleanup = func() {
			name := f.Name()
			_ = f.Close()
			fmt.Fprintf(os.Stderr, "comparison-report: wrote %s\n", name)
		}
	default:
		if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "comparison-report: %v\n", err)
			os.Exit(2)
		}
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "comparison-report: %v\n", err)
			os.Exit(2)
		}
		w = f
		cleanup = func() { _ = f.Close() }
	}
	defer cleanup()

	switch strings.ToLower(*format) {
	case "json":
		if err := comparison.WriteJSON(w, rep); err != nil {
			fmt.Fprintf(os.Stderr, "comparison-report: %v\n", err)
			os.Exit(2)
		}
	case "markdown", "md":
		if err := comparison.WriteMarkdown(w, rep); err != nil {
			fmt.Fprintf(os.Stderr, "comparison-report: %v\n", err)
			os.Exit(2)
		}
	default:
		fmt.Fprintf(os.Stderr, "comparison-report: unknown format %q\n", *format)
		os.Exit(2)
	}
}

func loadDoc(fixture string) (comparison.InputDocument, error) {
	if strings.TrimSpace(fixture) == "" {
		return comparison.SyntheticDocument(), nil
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		return comparison.InputDocument{}, err
	}
	doc, err := comparison.LoadInputBytes(raw)
	if err != nil {
		return comparison.InputDocument{}, err
	}
	return doc, nil
}
