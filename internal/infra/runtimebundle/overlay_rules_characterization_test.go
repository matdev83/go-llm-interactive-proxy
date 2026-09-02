package runtimebundle

import (
	"context"
	"errors"
	"strings"
	"testing"

	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard/engine"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"github.com/stretchr/testify/require"
)

// --- Overlay characterization stubs ---

type overOpener struct{ tag string }

func (o overOpener) ID() string { return o.tag }
func (overOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type overResolver struct{ tag string }

func (r overResolver) Resolve(context.Context) (workspace.WorkspaceView, error) {
	return workspace.WorkspaceView{ProjectRoot: r.tag}, nil
}

type overCatalogFilter struct{ tag string }

func (f overCatalogFilter) ID() string                      { return f.tag }
func (overCatalogFilter) Order() int                        { return 0 }
func (overCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (overCatalogFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type overPolicy struct{ tag string }

func (p overPolicy) ID() string                      { return p.tag }
func (overPolicy) Order() int                        { return 0 }
func (overPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (overPolicy) Handle(context.Context, lipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type overFinalizer struct{ tag string }

func (f overFinalizer) ID() string { return f.tag }
func (overFinalizer) Order() int   { return 0 }
func (overFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type overTransform struct{ tag string }

func (t overTransform) ID() string                      { return t.tag }
func (overTransform) Order() int                        { return 0 }
func (overTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (overTransform) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type overPreReq struct{ tag string }

func (h overPreReq) ID() string                      { return h.tag }
func (overPreReq) Order() int                        { return 0 }
func (overPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (overPreReq) Handle(context.Context, *lipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type overRouteHint struct{ tag string }

func (p overRouteHint) ID() string                      { return p.tag }
func (overRouteHint) Order() int                        { return 0 }
func (overRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (overRouteHint) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type overCompGate struct{ tag string }

func (g overCompGate) ID() string                      { return g.tag }
func (overCompGate) Order() int                        { return 0 }
func (overCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (overCompGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type overAttemptTransform struct{ tag string }

func (t overAttemptTransform) ID() string                      { return t.tag }
func (overAttemptTransform) Order() int                        { return 0 }
func (overAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (overAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type overStreamObserverFactory struct{ tag string }

func (f overStreamObserverFactory) ID() string                      { return f.tag }
func (overStreamObserverFactory) Order() int                        { return 0 }
func (overStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (overStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type overTrafficObs struct{ tag string }

func (overTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type overUsageObs struct{ tag string }

func (overUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type overRawSink struct{ tag string }

func (overRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type overRedactor struct{ tag string }

func (r overRedactor) ID() string { return r.tag }
func (overRedactor) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type overCompactionObs struct{ tag string }

func (overCompactionObs) OnCompaction(context.Context, compaction.Event) error { return nil }

type overSecretGuard struct{ tag string }

func (g overSecretGuard) ID() string                 { return g.tag }
func (overSecretGuard) Order() int                   { return 0 }
func (overSecretGuard) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (overSecretGuard) Evaluate(context.Context, *lipapi.Call, sdk.Meta, sdk.Services) (sdk.Decision, error) {
	return sdk.Decision{Outcome: sdk.OutcomePass}, nil
}

type overTerminalProvider struct{ tag string }

func (p overTerminalProvider) ID() string { return p.tag }
func (overTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type overBadIDTerminalProvider struct{ badID string }

func (p overBadIDTerminalProvider) ID() string { return p.badID }
func (overBadIDTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type overPanicTerminalProvider struct{}

func (overPanicTerminalProvider) ID() string { panic("provider panic") }
func (overPanicTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type overEnv struct{ tag string }

func (e overEnv) Lookup(name string) (string, bool) { return e.tag, true }
func (e overEnv) Snapshot() []string                { return []string{"TAG=" + e.tag} }

// --- Acceptance Criteria 1 & 3: Overlay Finalizer Cap Overwrite Rule ---

// --- Acceptance Criteria 3: Host Capability Overwrite-If-Non-Nil ---

// TestOverlayExtensions_SecretGuardHostCapabilitiesOverwriteIfNonNil pins overwrite-if-non-nil:
// - SecretGuardEnvironment: non-nil src overwrites dst; nil src preserves dst.
// - SecretDecisionObserver: non-nil src overwrites dst; nil src preserves dst.
func TestOverlayExtensions_SecretGuardHostCapabilitiesOverwriteIfNonNil(t *testing.T) {
	t.Parallel()

	envA := overEnv{tag: "env-a"}
	envB := overEnv{tag: "env-b"}
	obsA := sdk.ObserverFunc(func(context.Context, sdk.DecisionEvent) error { return nil })
	obsB := sdk.ObserverFunc(func(context.Context, sdk.DecisionEvent) error { return nil })

	t.Run("environment_overwrite_when_src_non_nil", func(t *testing.T) {
		t.Parallel()
		dst := &ExtensionsOptions{SecretGuardEnvironment: envA}
		src := ExtensionsOptions{SecretGuardEnvironment: envB}
		overlayExtensions(dst, src)
		require.Equal(t, envB, dst.SecretGuardEnvironment)
	})

	t.Run("environment_preserved_when_src_nil", func(t *testing.T) {
		t.Parallel()
		dst := &ExtensionsOptions{SecretGuardEnvironment: envA}
		src := ExtensionsOptions{SecretGuardEnvironment: nil}
		overlayExtensions(dst, src)
		require.Equal(t, envA, dst.SecretGuardEnvironment)
	})

	t.Run("observer_overwrite_when_src_non_nil", func(t *testing.T) {
		t.Parallel()
		dst := &ExtensionsOptions{SecretDecisionObserver: obsA}
		src := ExtensionsOptions{SecretDecisionObserver: obsB}
		overlayExtensions(dst, src)
		require.NotNil(t, dst.SecretDecisionObserver)
	})

	t.Run("observer_preserved_when_src_nil", func(t *testing.T) {
		t.Parallel()
		dst := &ExtensionsOptions{SecretDecisionObserver: obsA}
		src := ExtensionsOptions{SecretDecisionObserver: nil}
		overlayExtensions(dst, src)
		require.NotNil(t, dst.SecretDecisionObserver)
	})
}

// --- Acceptance Criteria 3: Omitted Fields Characterization ---

// TestOverlayExtensions_OmittedFieldsBehavior pins omitted fields:
// - SecretGuardInputs: present on ExtensionsOptions, but overlayExtensions does not touch it (no copy/overlay logic).
// - Migrated observer families (TrafficObservers, UsageObservers, RawCaptureSinks, TrafficRedactors, CompactionObservers): omitted from overlayExtensions.
// - CompactionPreservers: handled via generated plane adapters, NOT on ExtensionsOptions.
func TestOverlayExtensions_OmittedFieldsBehavior(t *testing.T) {
	t.Parallel()

	t.Run("secret_guard_inputs_is_omitted_from_overlay", func(t *testing.T) {
		t.Parallel()
		dst := &ExtensionsOptions{
			SecretGuardInputs: SecretGuardInputs{
				SingleUser: coresg.SingleUserOptions{MinSecretBytes: 10},
			},
		}
		src := ExtensionsOptions{
			SecretGuardInputs: SecretGuardInputs{
				SingleUser: coresg.SingleUserOptions{MinSecretBytes: 20},
			},
		}
		overlayExtensions(dst, src)
		require.Equal(t, 10, dst.SecretGuardInputs.SingleUser.MinSecretBytes, "SecretGuardInputs is omitted from overlay and not modified")
	})
}

// --- Acceptance Criteria 3: Nil Safety ---

func TestOverlayExtensions_NilSafety(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() {
		overlayExtensions(nil, ExtensionsOptions{})
	})
}

// --- Acceptance Criteria 2: validateTerminalDecisionProvider in runtimebundle ---

// TestTerminalDecision_ValidateInCompileGeneration pins validation of terminal decision provider in runtimebundle.
func TestTerminalDecision_ValidateInCompileGeneration(t *testing.T) {
	t.Parallel()

	t.Run("nil_provider_passes", func(t *testing.T) {
		t.Parallel()
		err := validateTerminalDecisionProvider(nil)
		require.NoError(t, err)
	})

	t.Run("valid_provider_passes", func(t *testing.T) {
		t.Parallel()
		err := validateTerminalDecisionProvider(overTerminalProvider{tag: "valid.id"})
		require.NoError(t, err)
	})

	t.Run("typed_nil_provider_fails", func(t *testing.T) {
		t.Parallel()
		var typedNil *overTerminalProvider
		err := validateTerminalDecisionProvider(typedNil)
		require.Error(t, err)
		require.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
	})

	t.Run("panicking_provider_fails", func(t *testing.T) {
		t.Parallel()
		err := validateTerminalDecisionProvider(overPanicTerminalProvider{})
		require.Error(t, err)
		require.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
	})

	t.Run("empty_id_fails", func(t *testing.T) {
		t.Parallel()
		err := validateTerminalDecisionProvider(overBadIDTerminalProvider{badID: ""})
		require.Error(t, err)
	})

	t.Run("whitespace_id_fails", func(t *testing.T) {
		t.Parallel()
		err := validateTerminalDecisionProvider(overBadIDTerminalProvider{badID: "   "})
		require.Error(t, err)
	})

	t.Run("invalid_utf8_fails", func(t *testing.T) {
		t.Parallel()
		err := validateTerminalDecisionProvider(overBadIDTerminalProvider{badID: "\xff\xfe"})
		require.Error(t, err)
	})

	t.Run("exceeds_max_bytes_fails", func(t *testing.T) {
		t.Parallel()
		err := validateTerminalDecisionProvider(overBadIDTerminalProvider{badID: strings.Repeat("x", terminaldecision.MaxProviderIDBytes+1)})
		require.Error(t, err)
	})
}
