package conformance

// ACP matrix cells execute against the relocated connector architecture.
//
// origin/main relocated the ACP HTTP prompt-turn adapter out of
// internal/plugins/backends/acp into the shared connector-support/acp module
// plus the executable connector module connectors/acp. The root module must not
// require connector modules (TestRootGoMod_NoConnectorModules) and production
// core must not import connector modules (hybrid-backend rules), so the
// conformance harness cannot link the ACP protocol adapter in-process. Instead
// it builds the actual lip-backend-acp executable (once per test binary) and
// drives each ACP backend through the backendplugin host adapter APIs
// (adapter.DialConfiguredSession + adapter.Build): the connector process is the
// real relocated connector, configured with the cell's observing origin as
// base_url, and the host adapter builds the execbackend.Backend exactly like the
// production composition. No ACP protocol code is duplicated in the harness.
//
// Each ACP backend owns a dedicated connector process. The process is shut down
// via tb.Cleanup, so parallel conformance cells stay isolated and no connector
// process outlives its test.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

var (
	acpConnectorOnce     sync.Once
	acpConnectorBin      string
	acpConnectorBinErr   error
	acpConnectorBuildLog string
)

// acpConnectorBackend launches a dedicated lip-backend-acp connector process
// configured against originURL and returns the host-built execbackend.Backend.
// The connector executable is built once per test binary; one process is
// launched per backend so the harness never shares connector state across cells.
func acpConnectorBackend(tb testing.TB, originURL string) execbackend.Backend {
	tb.Helper()
	bin := acpConnectorBinary(tb)
	cmd, addr := acpConnectorLaunch(tb, bin)
	ctx, cancel := context.WithCancel(context.Background())

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		cancel()
		acpConnectorKill(cmd)
		tb.Fatalf("harness: dial acp connector %s: %v", addr, err)
	}

	const instanceID = "harness-acp"
	policy := backendplugin.RuntimePolicy{
		MaxRequestBytes:     backendplugin.DefaultMaxMessageBytes,
		MaxStreamFrameBytes: backendplugin.DefaultMaxStreamFrameBytes,
	}
	cfg := "base_url: " + originURL + "\n"
	sess, profile, err := adapter.DialConfiguredSession(ctx, conn, instanceID, "acp", []byte(cfg), backendplugin.SecretBundle{}, policy)
	if err != nil {
		_ = conn.Close()
		cancel()
		acpConnectorKill(cmd)
		tb.Fatalf("harness: configure acp connector: %v", err)
	}

	br := adapter.Build(sess, profile, adapter.Options{
		InstanceID:    instanceID,
		RoutePrefixes: profile.RoutePrefixes,
		Negotiation:   sess.Negotiation(),
	})

	tb.Cleanup(func() {
		_ = br.Cleanup()
		cancel()
		acpConnectorKill(cmd)
	})
	return br.Backend
}

// acpConnectorBinary returns the path to a locally built connectors/acp
// executable, building it once per test binary. The build runs inside the
// connectors/acp module (GOWORK=off) so the root module graph stays independent
// of the connector modules.
func acpConnectorBinary(tb testing.TB) string {
	tb.Helper()
	acpConnectorOnce.Do(func() {
		acpConnectorBin, acpConnectorBinErr = buildACPConnectorBinary()
	})
	if acpConnectorBinErr != nil {
		if acpConnectorBuildLog != "" {
			tb.Fatalf("harness: build connectors/acp executable: %v\n%s", acpConnectorBinErr, acpConnectorBuildLog)
		}
		tb.Fatalf("harness: build connectors/acp executable: %v", acpConnectorBinErr)
	}
	return acpConnectorBin
}

func buildACPConnectorBinary() (string, error) {
	root, err := acpConnectorRepoRoot()
	if err != nil {
		return "", err
	}
	// Use the repo's allowlisted connector-build prefix so the leaked OS temp
	// dir (one per test binary, reclaimable by `make tmp-clean`) matches
	// scripts/tmp-clean.ps1 ownership.
	dir, err := os.MkdirTemp("", "lip-connector-build-*")
	if err != nil {
		return "", err
	}
	name := "lip-backend-acp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/lip-backend-acp")
	cmd.Dir = filepath.Join(root, "connectors", "acp")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		acpConnectorBuildLog = string(out)
		return "", err
	}
	return bin, nil
}

func acpConnectorRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "connectors", "acp", "go.mod")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repo root with connectors/acp not found from %s", wd)
		}
		dir = parent
	}
}

// acpConnectorLaunch starts a fresh lip-backend-acp process on an ephemeral
// loopback port and waits for its "listening on" address. The stderr pipe is
// drained for the process lifetime so the connector never blocks on a full pipe.
func acpConnectorLaunch(tb testing.TB, bin string) (*exec.Cmd, string) {
	tb.Helper()
	cmd := exec.Command(bin, "-listen", "127.0.0.1:0")
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LIP_PLUGIN_CHANNEL_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	stderr, err := cmd.StderrPipe()
	if err != nil {
		tb.Fatalf("harness: acp connector stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		tb.Fatalf("harness: start acp connector: %v", err)
	}
	addrCh := make(chan string, 1)
	go func() {
		sent := false
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if !sent {
				if after, ok := strings.CutPrefix(line, "lip-backend-acp listening on "); ok {
					sent = true
					addrCh <- strings.TrimSpace(after)
				}
			}
		}
		if !sent {
			addrCh <- ""
		}
	}()
	select {
	case addr := <-addrCh:
		if addr == "" {
			acpConnectorKill(cmd)
			tb.Fatalf("harness: acp connector did not report a listen address")
		}
		return cmd, addr
	case <-time.After(20 * time.Second):
		acpConnectorKill(cmd)
		tb.Fatalf("harness: timeout waiting for acp connector listen address")
	}
	return nil, ""
}

// acpConnectorKill best-effort terminates a launched connector process. It is
// safe to call from tb.Cleanup (never fails the test).
func acpConnectorKill(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
