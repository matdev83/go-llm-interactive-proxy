package conformance

// Connector-host harness for the optional connector matrix columns.
//
// origin/main relocated the executable backend connectors into their own modules
// (connectors/acp, connectors/openrouter, connectors/nvidia). The root module must
// not require connector modules (TestRootGoMod_NoConnectorModules) and production
// core must not import connector modules (hybrid-backend rules), so the conformance
// harness cannot link any connector protocol adapter in-process. Instead it builds
// the actual lip-backend-* executable of the optional connector (once per test
// binary, inside the connector module with GOWORK=off) and drives each backend
// through the backendplugin host adapter APIs (adapter.DialConfiguredSession +
// adapter.Build): the connector process is the real relocated connector,
// configured with the cell's observing origin as base_url, and the host adapter
// builds the execbackend.Backend exactly like the production composition. No
// connector protocol code is duplicated in the harness.
//
// This generalizes the original ACP-only connector harness (acp_connector.go) to
// every optional connector column the authoritative 5×9 matrix references:
// ACP (constructible through the base harness selector) and the OpenRouter/NVIDIA
// compatibility columns (driven through the dedicated connector-column deploy
// path, DeployConnectorColumnFor).
//
// Each backend owns a dedicated connector process. The process is shut down via
// tb.Cleanup, so parallel conformance cells stay isolated and no connector process
// outlives its test.

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

// connectorHostSpec identifies one optional executable backend-connector module
// the harness drives through the backendplugin host adapter.
type connectorHostSpec struct {
	// backendID is the authoritative matrix backend identity.
	backendID string
	// module is the connector module directory relative to the repo root.
	module string
	// cmd is the package (relative to the connector module) of the lip-backend-* binary.
	cmd string
	// bin is the produced executable base name (no OS suffix).
	bin string
	// listenMarker is the stderr prefix the connector prints when it is listening.
	listenMarker string
	// instanceID is the host adapter instance identity used for each backend.
	instanceID string
}

// connectorHostSpecs is the authoritative optional-connector table. It must stay
// exactly the matrix's optional connector columns and is pinned by the default-build
// connector_host_test.go. Adding an optional connector here is a deliberate matrix
// expansion (Task 8.5 owns authoritative list expansion), never a promotion to an
// essential backend kind.
var connectorHostSpecs = map[string]connectorHostSpec{
	BackendACP: {
		backendID:    BackendACP,
		module:       "connectors/acp",
		cmd:          "./cmd/lip-backend-acp",
		bin:          "lip-backend-acp",
		listenMarker: "lip-backend-acp listening on ",
		instanceID:   "harness-acp",
	},
	BackendOpenRouter: {
		backendID:    BackendOpenRouter,
		module:       "connectors/openrouter",
		cmd:          "./cmd/lip-backend-openrouter",
		bin:          "lip-backend-openrouter",
		listenMarker: "lip-backend-openrouter listening on ",
		instanceID:   "harness-openrouter",
	},
	BackendNVIDIA: {
		backendID:    BackendNVIDIA,
		module:       "connectors/nvidia",
		cmd:          "./cmd/lip-backend-nvidia",
		bin:          "lip-backend-nvidia",
		listenMarker: "lip-backend-nvidia listening on ",
		instanceID:   "harness-nvidia",
	},
}

func connectorHostBackendIDs() []string {
	return []string{BackendACP, BackendOpenRouter, BackendNVIDIA}
}

func connectorHostLookup(backendID string) (connectorHostSpec, bool) {
	spec, ok := connectorHostSpecs[backendID]
	return spec, ok
}

func connectorHostSpecRequired(tb testing.TB, backendID string) connectorHostSpec {
	tb.Helper()
	spec, ok := connectorHostLookup(backendID)
	if !ok {
		tb.Fatalf("harness: connector column %q has no connector host spec", backendID)
	}
	return spec
}

// connectorBuild is the built-once-per-test-binary connector executable state.
type connectorBuild struct {
	once sync.Once
	bin  string
	err  error
	log  string
}

var connectorHostBuilds sync.Map // backendID -> *connectorBuild

// connectorHostBackend launches a dedicated connector process for backendID
// configured against originURL and returns the host-built execbackend.Backend.
// The connector executable is built once per test binary; one process is launched
// per backend so the harness never shares connector state across cells.
func connectorHostBackend(tb testing.TB, backendID, originURL string) execbackend.Backend {
	tb.Helper()
	spec := connectorHostSpecRequired(tb, backendID)
	bin := connectorHostBinary(tb, backendID)
	cmd, addr := connectorHostLaunch(tb, bin, spec)
	ctx, cancel := context.WithCancel(context.Background())

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		cancel()
		connectorHostKill(cmd)
		tb.Fatalf("harness: dial %s connector %s: %v", backendID, addr, err)
	}

	policy := backendplugin.RuntimePolicy{
		MaxRequestBytes:     backendplugin.DefaultMaxMessageBytes,
		MaxStreamFrameBytes: backendplugin.DefaultMaxStreamFrameBytes,
	}
	cfgYAML, secrets := connectorHostConfig(spec, originURL)
	sess, profile, err := adapter.DialConfiguredSession(ctx, conn, spec.instanceID, backendID, cfgYAML, secrets, policy)
	if err != nil {
		_ = conn.Close()
		cancel()
		connectorHostKill(cmd)
		tb.Fatalf("harness: configure %s connector: %v", backendID, err)
	}

	br := adapter.Build(sess, profile, adapter.Options{
		InstanceID:    spec.instanceID,
		RoutePrefixes: profile.RoutePrefixes,
		Negotiation:   sess.Negotiation(),
	})

	tb.Cleanup(func() {
		_ = br.Cleanup()
		cancel()
		connectorHostKill(cmd)
	})
	return br.Backend
}

// connectorHostConfig builds the connector YAML and credential bundle for one
// optional connector against an observing origin. The OpenAI-compatible connector
// columns require an api_key (their Configure fails closed without one), so the
// harness supplies the same synthetic credential the essential harness backends
// use; the reference origins accept any non-empty bearer token.
func connectorHostConfig(spec connectorHostSpec, originURL string) ([]byte, backendplugin.SecretBundle) {
	switch spec.backendID {
	case BackendOpenRouter, BackendNVIDIA:
		return []byte("base_url: " + originURL + "\n"),
			backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("sk-test")}}
	default:
		return []byte("base_url: " + originURL + "\n"), backendplugin.SecretBundle{}
	}
}

// connectorHostBinary returns the path to a locally built optional connector
// executable, building it once per test binary. The build runs inside the
// connector module (GOWORK=off) so the root module graph stays independent of the
// connector modules.
func connectorHostBinary(tb testing.TB, backendID string) string {
	tb.Helper()
	spec := connectorHostSpecRequired(tb, backendID)
	cb := &connectorBuild{}
	actual, _ := connectorHostBuilds.LoadOrStore(backendID, cb)
	var ok bool
	if cb, ok = actual.(*connectorBuild); !ok {
		tb.Fatalf("harness: connector host build registry corrupted for %q", backendID)
	}
	cb.once.Do(func() {
		cb.bin, cb.log, cb.err = buildConnectorBinary(spec)
	})
	if cb.err != nil {
		if cb.log != "" {
			tb.Fatalf("harness: build %s executable: %v\n%s", spec.module, cb.err, cb.log)
		}
		tb.Fatalf("harness: build %s executable: %v", spec.module, cb.err)
	}
	return cb.bin
}

func buildConnectorBinary(spec connectorHostSpec) (bin string, log string, err error) {
	root, buildErr := connectorHostRepoRoot(spec.module)
	if buildErr != nil {
		return "", "", buildErr
	}
	// Use the repo's allowlisted connector-build prefix so the leaked OS temp
	// dir (one per test binary, reclaimable by `make tmp-clean`) matches
	// scripts/tmp-clean.ps1 ownership.
	dir, buildErr := os.MkdirTemp("", "lip-connector-build-*")
	if buildErr != nil {
		return "", "", buildErr
	}
	name := spec.bin
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin = filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, spec.cmd)
	cmd.Dir = filepath.Join(root, spec.module)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		return "", string(out), buildErr
	}
	return bin, "", nil
}

func connectorHostRepoRoot(module string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, module, "go.mod")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repo root with %s not found from %s", module, wd)
		}
		dir = parent
	}
}

// connectorHostLaunch starts a fresh connector process on an ephemeral loopback
// port and waits for its "listening on" address. The stderr pipe is drained for
// the process lifetime so the connector never blocks on a full pipe.
func connectorHostLaunch(tb testing.TB, bin string, spec connectorHostSpec) (*exec.Cmd, string) {
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
		tb.Fatalf("harness: %s connector stderr pipe: %v", spec.backendID, err)
	}
	if err := cmd.Start(); err != nil {
		tb.Fatalf("harness: start %s connector: %v", spec.backendID, err)
	}
	addrCh := make(chan string, 1)
	go func() {
		sent := false
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if !sent {
				if after, ok := strings.CutPrefix(line, spec.listenMarker); ok {
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
			connectorHostKill(cmd)
			tb.Fatalf("harness: %s connector did not report a listen address", spec.backendID)
		}
		return cmd, addr
	case <-time.After(20 * time.Second):
		connectorHostKill(cmd)
		tb.Fatalf("harness: timeout waiting for %s connector listen address", spec.backendID)
	}
	return nil, ""
}

// connectorHostKill best-effort terminates a launched connector process. It is
// safe to call from tb.Cleanup (never fails the test).
func connectorHostKill(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
