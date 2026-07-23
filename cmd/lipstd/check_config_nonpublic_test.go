package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

// TestCheckConfig_NonPublicNoListenAndPrivateCleanup proves check-config validates
// via the serve composer/compile path without binding a data-plane listener, and
// that the command's private generation/process owners are cleaned up afterward.
// True unpublished ValidateDistribution (no manager publish) remains task 5.4.
//
// Not parallel: wraps package-global cleanupCheckConfigBootstrap.
func TestCheckConfig_NonPublicNoListenAndPrivateCleanup(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()

	orig := cleanupCheckConfigBootstrap
	t.Cleanup(func() { cleanupCheckConfigBootstrap = orig })

	var (
		cleanupCalls int
		owned        runtimebundle.BootstrapResult
	)
	cleanupCheckConfigBootstrap = func(ctx context.Context, res *runtimebundle.BootstrapResult) {
		// Record only this test's probe address so parallel sibling check-config
		// tests (if any race the hook) cannot pollute the observation.
		if res != nil && res.Config != nil && res.Config.Server.Address == addr {
			cleanupCalls++
			owned = *res
		}
		orig(ctx, res)
	}

	cfgPath := writeCheckConfigListenProbeConfig(t, addr)

	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("check-config exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout=%q", out.String())
	}

	// If check-config had attempted to listen on the configured address, the
	// pre-bound exclusive listener would cause bind failure / non-zero exit.
	// Accept one more connection attempt into our listener to prove ownership.
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(50 * time.Millisecond))
	if conn, acceptErr := ln.Accept(); acceptErr == nil {
		_ = conn.Close()
		t.Fatal("unexpected accept: check-config must not dial or hand off the data-plane address")
	}

	if cleanupCalls != 1 {
		t.Fatalf("cleanupCheckConfigBootstrap invocations for probe addr=%q: got %d want 1", addr, cleanupCalls)
	}
	if owned.GenerationManager == nil || owned.ProcessServices == nil {
		t.Fatal("command-owned check-config path must expose generation manager and process services")
	}
	if owned.GenerationManager.HasOpenGenerations() {
		t.Fatal("command-owned cleanup must leave no open generations")
	}
	if !owned.ProcessServices.Closed() {
		t.Fatal("command-owned process services must be closed after cleanup")
	}
}

func writeCheckConfigListenProbeConfig(t *testing.T, listenAddr string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	const old = `address: "127.0.0.1:18080"`
	neu := fmt.Sprintf(`address: %q`, listenAddr)
	if !strings.Contains(text, old) {
		t.Fatalf("dogfood fixture missing %q", old)
	}
	text = strings.Replace(text, old, neu, 1)
	path := filepath.Join(t.TempDir(), "check-config-nolisten.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
