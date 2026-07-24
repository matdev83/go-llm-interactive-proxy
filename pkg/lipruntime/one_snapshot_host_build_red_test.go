package lipruntime_test

import (
	"context"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// TestHostBuild_PublicBuildIsOneOperation fails while public Build still requires
// BuildBootstrap + AttachReloadHost (req 4.1, 4.5, 4.8). Architecture allowlist
// owns call-site enforcement; this behavioral contract observes that a successful
// BootstrapServe result still needs AttachReloadHost before a coordinator exists.
func TestHostBuild_PublicBuildIsOneOperation(t *testing.T) {
	t.Parallel()
	assertPublicBuildStillTwoStep(t)
}

// TestOneSnapshot_PublicBuildStillTwoStepOwnership narrows the public facade claim
// to the two-step ownership fact. Fingerprint consistency across controlled A/B
// snapshots is owned by runtimebundle TestOneSnapshot_HostTransactionSharesAcceptedSnapshot.
func TestOneSnapshot_PublicBuildStillTwoStepOwnership(t *testing.T) {
	t.Parallel()
	assertPublicBuildStillTwoStep(t)
}

func assertPublicBuildStillTwoStep(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	cfgPath := repoConfigPath(t)

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
	t.Cleanup(func() { cleanupPublicBootstrap(t, res) })
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
	t.Fatalf("public Build must obtain a complete Host from one host-build call; BootstrapResult still requires AttachReloadHost (req 4.1, 4.5)")
}

func cleanupPublicBootstrap(t *testing.T, res runtimebundle.BootstrapResult) {
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
