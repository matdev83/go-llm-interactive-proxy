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
	planeOutPath := *outPathFlag
	if planeOutPath == "" {
		planeOutPath = filepath.Join(repoRoot, "pkg", "lipsdk", "feature", "plane_generated.go")
	}
	bundleOutPath := filepath.Join(repoRoot, "internal", "featurebundle", "bundle_projection_generated.go")

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading manifest %s: %v\n", manifestPath, err)
		os.Exit(1)
	}

	formattedPlanes, err := archtest.GenerateFeaturePlanesCode(manifestBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating feature planes code: %v\n", err)
		os.Exit(1)
	}

	formattedBundle, err := archtest.GenerateFeatureBundleProjectionCode(manifestBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating bundle projection code: %v\n", err)
		os.Exit(1)
	}

	if *checkFlag {
		checkFileMatches(planeOutPath, formattedPlanes)
		checkFileMatches(bundleOutPath, formattedBundle)
		fmt.Println("generated feature planes and bundle projection files are up to date.")
		return
	}

	if err := archtest.WriteGeneratedPairAtomic(planeOutPath, formattedPlanes, bundleOutPath, formattedBundle); err != nil {
		fmt.Fprintf(os.Stderr, "error writing generated files atomically: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s and %s successfully.\n", planeOutPath, bundleOutPath)
}

func checkFileMatches(path string, formatted []byte) {
	existing, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading existing generated file %s: %v\n", path, err)
		os.Exit(1)
	}
	normExisting := bytes.ReplaceAll(existing, []byte("\r\n"), []byte("\n"))
	normFormatted := bytes.ReplaceAll(formatted, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(normExisting, normFormatted) {
		fmt.Fprintf(os.Stderr, "ERROR: generated file %s is stale or differs from manifest\nRun 'go run ./scripts/generate-feature-planes.go' to regenerate.\n", path)
		os.Exit(1)
	}
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
