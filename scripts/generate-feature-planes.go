//go:build ignore

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/matdev83/go-llm-interactive-proxy/internal/archtest"
)

func main() {
	checkFlag := flag.Bool("check", false, "verify that generated output matches disk without modifying files")
	manifestPathFlag := flag.String("manifest", "", "path to plane_manifest.go (default: auto-detected)")
	outPathFlag := flag.String("out", "", "path to plane_generated.go (default: auto-detected)")
	flag.Parse()

	repoRoot := findRepoRoot()
	manifestPath := *manifestPathFlag
	if manifestPath == "" {
		manifestPath = filepath.Join(repoRoot, "pkg", "lipsdk", "feature", "plane_manifest.go")
	}
	outPath := *outPathFlag
	if outPath == "" {
		outPath = filepath.Join(repoRoot, "pkg", "lipsdk", "feature", "plane_generated.go")
	}

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading manifest %s: %v\n", manifestPath, err)
		os.Exit(1)
	}

	formatted, err := archtest.GenerateFeaturePlanesCode(manifestBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating code: %v\n", err)
		os.Exit(1)
	}

	if *checkFlag {
		existing, err := os.ReadFile(outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading existing generated file %s: %v\n", outPath, err)
			os.Exit(1)
		}
		if !bytes.Equal(existing, formatted) {
			fmt.Fprintf(os.Stderr, "ERROR: generated file %s is stale or differs from manifest\nRun 'go run ./scripts/generate-feature-planes.go' to regenerate.\n", outPath)
			os.Exit(1)
		}
		fmt.Println("plane_generated.go is up to date.")
		return
	}

	if err := os.WriteFile(outPath, formatted, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing generated file %s: %v\n", outPath, err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s successfully.\n", outPath)
}

func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}
