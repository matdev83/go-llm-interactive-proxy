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
)

// TestCheckConfig_NonPublicNoListenAndPrivateCleanup proves check-config
// validates via the serve composer/compile path (runtimebundle.ValidateDistribution)
// without binding a data-plane listener, and never publishes a generation:
// [runtimebundle.ValidateDistribution] owns and closes every resource it
// acquires internally, so there is no command-owned handle left to inspect
// (Task 5.4; design Dry-Run Validation).
func TestCheckConfig_NonPublicNoListenAndPrivateCleanup(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()

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

	// check-config must be repeatable against the same configured address
	// without ever leaving a Manager, generation, or listener behind.
	var out2, errb2 bytes.Buffer
	code2 := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out2,
		ErrorOut:   &errb2,
	})
	if code2 != 0 {
		t.Fatalf("second check-config exit %d stderr=%s", code2, errb2.String())
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
