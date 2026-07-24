package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// TestHostBuild_ServeUsesSingleHostBuildCall fails while serve-shaped startup
// still requires BuildBootstrap + AttachReloadHost (req 4.1, 4.5). Architecture
// scanners keep production allowlisted until Task 5.5; this behavioral contract
// stays RED until BuildHost migrates the serve path.
func TestHostBuild_ServeUsesSingleHostBuildCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")

	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath:      cfgPath,
		Mode:            runtimebundle.BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildBootstrap: %v", err)
	}
	t.Cleanup(func() { cleanupServeBootstrap(t, res) })
	if res.GenerationManager == nil || res.ProcessServices == nil {
		t.Fatal("BootstrapServe must publish generation host handles")
	}

	host, err := runtimebundle.AttachReloadHost(ctx, res, cfgPath, stdhttp.ComposeStandardHTTP)
	if err != nil {
		t.Fatalf("AttachReloadHost: %v", err)
	}
	if host == nil || host.Coordinator == nil {
		t.Fatal("AttachReloadHost must bind coordinator in current architecture")
	}
	// Observed: coordinator binding required a second ownership step after bootstrap.
	t.Fatalf("serve must obtain a complete Host from one BuildHost call; runServeCommand still requires BuildBootstrap+AttachReloadHost (req 4.1, 4.5)")
}

// TestOneSnapshot_ServePathMustNotDoubleLoadEffective fails while the serve gate
// and BuildBootstrap can observe disagreeing snapshots across a config mutation
// (req 4.2-4.3). Controlled A/B load counts for HostBuilder live in runtimebundle.
func TestOneSnapshot_ServePathMustNotDoubleLoadEffective(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := writeServeMarkerConfig(t, "127.0.0.1:18301", accessmode.ModeMultiUser)

	flagTrue := true
	if err := validateServeMultiUserGate(ctx, path, &flagTrue, config.StreamRecoveryOverrides{}); err != nil {
		t.Fatalf("gate with snapshot A: %v", err)
	}
	gateEff, err := runtimebundle.LoadBootstrapEffective(ctx, path, config.StreamRecoveryOverrides{})
	if err != nil {
		t.Fatalf("capture gate fingerprint: %v", err)
	}
	gateFP := gateEff.Identity.PublicFingerprint

	rewriteServeMarkerConfig(t, path, "127.0.0.1:18302", accessmode.ModeSingleUser)

	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath:      path,
		Mode:            runtimebundle.BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildBootstrap after mutation: %v", err)
	}
	t.Cleanup(func() { cleanupServeBootstrap(t, res) })

	genFP := ""
	if res.InitialGeneration != nil {
		genFP = res.InitialGeneration.Status().Meta.PublicFingerprint
	}
	if gateFP == "" || genFP == "" {
		t.Fatal("expected non-empty fingerprints")
	}
	if gateFP == genFP {
		t.Fatal("expected TOCTOU disagreement after controlled config mutation")
	}
	t.Fatalf("serve path must not double-load effective config; gate fingerprint=%s generation fingerprint=%s (req 4.2-4.3)", gateFP, genFP)
}

func cleanupServeBootstrap(t *testing.T, res runtimebundle.BootstrapResult) {
	t.Helper()
	ctx := context.Background()
	if res.GenerationManager != nil {
		_ = res.GenerationManager.ShutdownDetached(ctx, runtimehost.NewLifecycleWorker())
	}
	if res.ProcessServices != nil {
		_ = res.ProcessServices.Close()
	}
	if res.ShutdownTracing != nil {
		_ = res.ShutdownTracing(ctx)
	}
}

func writeServeMarkerConfig(t *testing.T, address string, mode accessmode.Mode) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "serve-one-snapshot.yaml")
	if err := os.WriteFile(path, applyServeMarker(t, string(base), address, mode), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func rewriteServeMarkerConfig(t *testing.T, path, address string, mode accessmode.Mode) {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, applyServeMarker(t, string(base), address, mode), 0o600); err != nil {
		t.Fatal(err)
	}
}

func applyServeMarker(t *testing.T, text, address string, mode accessmode.Mode) []byte {
	t.Helper()
	text = strings.Replace(text, `address: "127.0.0.1:18080"`, `address: "`+address+`"`, 1)
	if !strings.Contains(text, address) {
		t.Fatal("failed to rewrite server address marker")
	}
	if mode == accessmode.ModeMultiUser {
		insert := "" +
			"access:\n" +
			"  mode: multi_user\n" +
			"auth:\n" +
			"  handler: local_api_key\n" +
			"  required_level: api_key\n" +
			"  local_api_keys:\n" +
			"    - key_id: k1\n" +
			"      principal_id: p1\n" +
			"      key: \"test-key-at-least-16-chars\"\n"
		if strings.Contains(text, "\naccess:\n") {
			t.Fatal("dogfood fixture unexpectedly declares access block")
		}
		text = strings.Replace(text, "\nrouting:\n", "\n"+insert+"routing:\n", 1)
		if !strings.Contains(text, "auth_mode:") {
			text = strings.Replace(text, "server:\n", "server:\n  auth_mode: external\n", 1)
		}
	}
	return []byte(text)
}
