package runtimebundle_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestConfigExamples_passBootstrapInspect(t *testing.T) {
	t.Parallel()
	root := repoRootFromRuntimebundleTest(t)
	dir := filepath.Join(root, "config", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var yamls []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".yaml") || strings.HasSuffix(strings.ToLower(e.Name()), ".yml") {
			yamls = append(yamls, filepath.Join(dir, e.Name()))
		}
	}
	if len(yamls) == 0 {
		t.Fatal("no yaml files in config/examples")
	}
	mandatory := lipsdk.StandardDistributionRequirements()
	for _, path := range yamls {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
				ConfigPath: path,
				Mode:       runtimebundle.BootstrapInspect,
				Mandatory:  mandatory,
				LogWriter:  io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.ShutdownTracing != nil {
				_ = res.ShutdownTracing(context.Background())
			}
		})
	}
}

func repoRootFromRuntimebundleTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("go.mod not found above %s", wd)
	return ""
}
