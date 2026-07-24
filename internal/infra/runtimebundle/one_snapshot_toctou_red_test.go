package runtimebundle

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// TestTOCTOU_ServeGateAndBootstrapDisagreeAcrossControlledLoads proves the
// historical two-load defect is closed: BuildHost evaluates the multi-user gate
// against the same accepted snapshot used for generation/reload (req 4.2-4.3).
func TestTOCTOU_ServeGateAndBootstrapDisagreeAcrossControlledLoads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pathA := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18101", accessmode.ModeMultiUser)
	pathB := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18102", accessmode.ModeSingleUser)

	snapA := mustLoadBootstrapSnapshot(t, ctx, pathA)
	snapB := mustLoadBootstrapSnapshot(t, ctx, pathB)
	if snapA.eff.Identity.PublicFingerprint == "" || snapB.eff.Identity.PublicFingerprint == "" {
		t.Fatal("expected non-empty public fingerprints")
	}
	if snapA.eff.Identity.PublicFingerprint == snapB.eff.Identity.PublicFingerprint {
		t.Fatal("controlled snapshots A/B must be distinguishable by public fingerprint")
	}
	if snapA.active.HandleIdentity == snapB.active.HandleIdentity {
		t.Fatal("controlled snapshots A/B must be distinguishable by source handle identity")
	}

	var loads atomic.Int32
	load := func(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error) {
		n := loads.Add(1)
		switch n {
		case 1:
			return snapA.eff, snapA.active, snapA.fixed, nil
		default:
			return snapB.eff, snapB.active, snapB.fixed, nil
		}
	}

	flagTrue := true
	out, err := buildHostOutcome(ctx, hostBuildInput{
		ConfigPath:              pathA,
		Mandatory:               lipsdk.StandardDistributionRequirements(),
		LogWriter:               io.Discard,
		HandlerComposer:         stubHandlerComposer,
		EnforceMultiUserCLIGate: true,
		MultiUser:               &flagTrue,
	}, load)
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { cleanupReloadHost(t, out.Host) })

	gotLoads := int(loads.Load())
	wantFP := snapA.eff.Identity.PublicFingerprint
	genFP := ""
	if out.Host.Manager != nil && out.Host.Manager.Active() != nil {
		genFP = out.Host.Manager.Active().Status().Meta.PublicFingerprint
	}
	processAddr := ""
	if out.Host.Config != nil {
		processAddr = out.Host.Config.Server.Address
	}
	reloadFP := ""
	if out.Host.Effective != nil {
		reloadFP = out.Host.Effective.Identity.PublicFingerprint
	}
	reloadHandle := configsource.FileIdentity{}
	if out.Host.ActiveSource != nil {
		reloadHandle = out.Host.ActiveSource.HandleIdentity
	}

	var problems []string
	if gotLoads != 1 || out.Journal.Loads != 1 {
		problems = append(problems, "effective loads="+strconv.Itoa(gotLoads)+" journal="+strconv.Itoa(out.Journal.Loads)+" want 1")
	}
	if wantFP != genFP {
		problems = append(problems, "gate fingerprint="+wantFP+" generation fingerprint="+genFP)
	}
	if wantFP != reloadFP {
		problems = append(problems, "gate fingerprint="+wantFP+" reload Effective fingerprint="+reloadFP)
	}
	if snapA.active.HandleIdentity != reloadHandle {
		problems = append(problems, "reload ActiveSource handle differs from accepted snapshot A handle")
	}
	if processAddr != snapA.eff.Config.Server.Address {
		problems = append(problems, "process address="+processAddr+" want snapshot A address="+snapA.eff.Config.Server.Address)
	}
	if !hostIsComplete(out) {
		problems = append(problems, "incomplete Host")
	}
	if len(problems) != 0 {
		t.Fatalf("one-snapshot HostBuild invariant failed (%d):\n- %s", len(problems), strings.Join(problems, "\n- "))
	}
}

// TestOneSnapshot_HostTransactionSharesAcceptedSnapshot is the desired HostBuilder
// contract (req 4.1-4.4).
func TestOneSnapshot_HostTransactionSharesAcceptedSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pathA := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18201", accessmode.ModeSingleUser)
	pathB := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18202", accessmode.ModeSingleUser)
	snapA := mustLoadBootstrapSnapshot(t, ctx, pathA)
	snapB := mustLoadBootstrapSnapshot(t, ctx, pathB)

	var loads atomic.Int32
	load := func(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error) {
		if loads.Add(1) == 1 {
			return snapA.eff, snapA.active, snapA.fixed, nil
		}
		return snapB.eff, snapB.active, snapB.fixed, nil
	}

	flagFalse := false
	out, err := buildHostOutcome(ctx, hostBuildInput{
		ConfigPath:      pathA,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
		MultiUser:       &flagFalse,
	}, load)
	if err != nil {
		t.Fatalf("HostBuilder transaction must succeed with one accepted snapshot: %v", err)
	}
	t.Cleanup(func() { cleanupReloadHost(t, out.Host) })

	wantFP := snapA.eff.Identity.PublicFingerprint
	wantAddr := snapA.eff.Config.Server.Address
	if out.Journal.Loads != 1 || loads.Load() != 1 {
		t.Fatalf("HostBuilder must read effective config exactly once; journal.loads=%d loader.loads=%d (req 4.2)", out.Journal.Loads, loads.Load())
	}
	if !hostIsComplete(out) {
		t.Fatalf("HostBuilder must return one complete Host (req 4.1, 4.5)")
	}
	if out.Host.Effective == nil || out.Host.Effective.Identity.PublicFingerprint != wantFP {
		t.Fatalf("reload Effective fingerprint mismatch want %q", wantFP)
	}
	if out.Host.Effective.Config == nil || out.Host.Effective.Config.Server.Address != wantAddr {
		t.Fatalf("process runtime config must derive from accepted snapshot")
	}
	if out.Host.Manager == nil || out.Host.Manager.Active() == nil {
		t.Fatal("complete Host must publish generation 1")
	}
	if out.Host.Manager.Active().Status().Meta.PublicFingerprint != wantFP {
		t.Fatalf("generation 1 fingerprint must derive from accepted snapshot")
	}
	if out.Host.Source == nil {
		t.Fatal("complete Host must bind ActiveSource identity")
	}
}

type bootstrapSnapshot struct {
	eff    *config.EffectiveConfig
	active *configsource.ActiveSourceVersion
	fixed  config.StreamRecoveryOverrides
}

func mustLoadBootstrapSnapshot(t *testing.T, ctx context.Context, path string) bootstrapSnapshot {
	t.Helper()
	eff, active, fixed, err := LoadBootstrapEffectiveWithSource(ctx, path, config.StreamRecoveryOverrides{})
	if err != nil {
		t.Fatalf("preload %s: %v", path, err)
	}
	if eff == nil || active == nil {
		t.Fatal("preload returned nil effective/active")
	}
	return bootstrapSnapshot{eff: eff, active: active, fixed: fixed}
}

func writeOneSnapshotMarkerConfig(t *testing.T, address string, mode accessmode.Mode) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(base)
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
		if !strings.Contains(text, "mode: multi_user") {
			t.Fatal("failed to inject multi_user access mode")
		}
		if !strings.Contains(text, "auth_mode:") {
			text = strings.Replace(text, "server:\n", "server:\n  auth_mode: external\n", 1)
		}
	}
	path := filepath.Join(t.TempDir(), "one-snapshot.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func cleanupReloadHost(t *testing.T, host *ReloadHost) {
	t.Helper()
	if host == nil {
		return
	}
	_ = host.Close(context.Background())
}
