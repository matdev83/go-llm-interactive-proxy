package secretguardcompose_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretguardcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

type panicEnv struct{ calls int }

func (p *panicEnv) Lookup(string) (string, bool) {
	p.calls++
	panic("unexpected environment lookup")
}

func (p *panicEnv) Snapshot() []string {
	p.calls++
	panic("unexpected environment snapshot")
}

type mapEnv struct {
	vals map[string]string
}

func (m *mapEnv) Lookup(name string) (string, bool) {
	v, ok := m.vals[name]
	return v, ok
}

func (m *mapEnv) Snapshot() []string {
	out := make([]string, 0, len(m.vals))
	for k, v := range m.vals {
		out = append(out, k+"="+v)
	}
	return out
}

type stubGuard struct {
	id string
}

func (g stubGuard) ID() string                   { return g.id }
func (g stubGuard) Order() int                   { return 1 }
func (g stubGuard) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (g stubGuard) Evaluate(context.Context, *lipapi.Call, sdk.Meta, sdk.Services) (sdk.Decision, error) {
	return sdk.Decision{Outcome: sdk.OutcomePass}, nil
}

type customObserver struct {
	called bool
}

func (o *customObserver) OnSecretDecision(context.Context, sdk.DecisionEvent) error {
	o.called = true
	return nil
}

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSecretGuardCompose_UnknownAccessModeFailsClosed(t *testing.T) {
	t.Parallel()

	unknownModes := []accessmode.Mode{
		"invalid",
		"single-user",
		"cluster",
		"root",
		"unknown",
	}

	for _, mode := range unknownModes {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			_, err := secretguardcompose.Compose(secretguardcompose.Input{
				AccessMode: mode,
				Logger:     discardLogger(),
			})
			if err == nil {
				t.Fatalf("expected error for unknown access mode %q, got nil", mode)
			}
			if !strings.Contains(err.Error(), "access mode") {
				t.Fatalf("expected error to mention access mode, got: %v", err)
			}
		})
	}
}

func TestSecretGuardCompose_MultiUserZeroEnvironmentCalls(t *testing.T) {
	t.Parallel()
	env := &panicEnv{}
	raw := mustYAMLNode(t, "action: block\n")
	regs := []lipsdk.Registration{{
		Kind:        lipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: raw},
	}}

	out, err := secretguardcompose.Compose(secretguardcompose.Input{
		AccessMode:    accessmode.ModeMultiUser,
		Registrations: regs,
		Environment:   env,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.calls != 0 {
		t.Fatalf("expected 0 env calls in multi_user mode, got %d", env.calls)
	}
	if out.Plane.AccessMode != "multi_user" {
		t.Fatalf("plane access mode: got %q, want multi_user", out.Plane.AccessMode)
	}
}

func TestSecretGuardCompose_DisabledZeroEnvironmentCalls(t *testing.T) {
	t.Parallel()
	env := &panicEnv{}
	out, err := secretguardcompose.Compose(secretguardcompose.Input{
		AccessMode:  accessmode.ModeSingleUser,
		Environment: env,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.calls != 0 {
		t.Fatalf("expected 0 env calls when feature disabled, got %d", env.calls)
	}
	if out.Inventory != nil {
		t.Fatalf("expected nil inventory when feature disabled and no guards, got %#v", out.Inventory)
	}
}

func TestSecretGuardCompose_SingleUserReadsEnvironment(t *testing.T) {
	t.Parallel()
	secret := "super-secret-password-1234"
	env := &mapEnv{vals: map[string]string{
		"MY_APP_SECRET": secret,
	}}
	raw := mustYAMLNode(t, `
action: redact
min_secret_bytes: 8
single_user:
  include_popular_env: false
  include_env: [MY_APP_SECRET]
`)
	regs := []lipsdk.Registration{{
		Kind:        lipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: raw},
	}}

	out, err := secretguardcompose.Compose(secretguardcompose.Input{
		AccessMode:    accessmode.ModeSingleUser,
		Registrations: regs,
		Environment:   env,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Plane.MatcherResolver == nil {
		t.Fatal("expected non-nil MatcherResolver")
	}
	matcher, err := out.Plane.MatcherResolver.Resolve(t.Context())
	if err != nil || matcher == nil {
		t.Fatalf("failed to resolve matcher: %v", err)
	}
	findings, err := matcher.ScanString(t.Context(), "key="+secret)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected finding for secret in single_user mode")
	}
	if out.Inventory == nil || out.Inventory.SecretGuardCatalogEntryCount < 1 {
		t.Fatalf("expected catalog entries in inventory, got %#v", out.Inventory)
	}
}

func TestSecretGuardCompose_NilLoggerFailsClosedWhenAuditRequired(t *testing.T) {
	t.Parallel()

	t.Run("feature_enabled", func(t *testing.T) {
		t.Parallel()
		raw := mustYAMLNode(t, "action: log\n")
		regs := []lipsdk.Registration{{
			Kind:        lipsdk.PluginKindFeature,
			ID:          "secrets-guard",
			FactoryKind: "secrets-guard",
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: raw},
		}}

		_, err := secretguardcompose.Compose(secretguardcompose.Input{
			AccessMode:    accessmode.ModeSingleUser,
			Registrations: regs,
			Logger:        nil,
		})
		if err == nil {
			t.Fatal("expected error with nil logger, got nil")
		}
		if err.Error() != "runtimebundle: secrets-guard audit requires a non-nil logger" {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("injected_guards", func(t *testing.T) {
		t.Parallel()
		_, err := secretguardcompose.Compose(secretguardcompose.Input{
			AccessMode: accessmode.ModeSingleUser,
			Guards:     []sdk.Guard{stubGuard{id: "custom"}},
			Logger:     nil,
		})
		if err == nil {
			t.Fatal("expected error with nil logger, got nil")
		}
		if err.Error() != "runtimebundle: secrets-guard audit requires a non-nil logger" {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSecretGuardCompose_InjectedGuardsWithExplicitObserverAndNilLoggerSucceeds(t *testing.T) {
	t.Parallel()
	obs := &customObserver{}
	out, err := secretguardcompose.Compose(secretguardcompose.Input{
		AccessMode:       accessmode.ModeSingleUser,
		Guards:           []sdk.Guard{stubGuard{id: "custom"}},
		DecisionObserver: obs,
		Logger:           nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Plane.DecisionObserver == nil {
		t.Fatal("expected non-nil DecisionObserver")
	}
}

func TestSecretGuardCompose_SingleUserHostOverrides(t *testing.T) {
	t.Parallel()
	runtimeCfg := secretguard.RuntimeConfig{
		Enabled:               true,
		PreserveKnownPrefixes: true,
		MaskByte:              '*',
		MinSecretBytes:        8,
	}
	inputs := secretguardcompose.SecretGuardInputs{
		SingleUser: secretguardcompose.SingleUserOptions{
			Matcher:           secretguardcompose.MatcherOptions{PreserveKnownPrefixes: false, MaskByte: 'X'},
			MatcherConfigured: true,
		},
	}
	out, err := secretguardcompose.Compose(secretguardcompose.Input{
		AccessMode:    accessmode.ModeSingleUser,
		RuntimeConfig: &runtimeCfg,
		Inputs:        inputs,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil output")
	}
}

func TestSecretGuardCompose_ValidateRegistrations_RejectsDuplicates(t *testing.T) {
	t.Parallel()
	regs := []lipsdk.Registration{
		{
			Kind:        lipsdk.PluginKindFeature,
			ID:          "secrets-guard-1",
			FactoryKind: "secrets-guard",
			Enabled:     true,
		},
		{
			Kind:        lipsdk.PluginKindFeature,
			ID:          "secrets-guard-2",
			FactoryKind: "secrets-guard",
			Enabled:     true,
		},
	}
	err := secretguardcompose.ValidateRegistrations(regs)
	if err == nil {
		t.Fatal("expected error on duplicate enabled secrets-guard, got nil")
	}
	if !strings.Contains(err.Error(), "multiple enabled secrets-guard registrations") {
		t.Fatalf("unexpected error: %v", err)
	}
}
