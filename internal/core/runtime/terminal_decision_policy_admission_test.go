package runtime

import (
	"context"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func TestSnapshotTerminalDecisionPolicyUsesGenerationDefaultWithoutSecureScope(t *testing.T) {
	provider := admissionProviderStub{}
	executor := admissionExecutor(provider)

	policy, enabled, err := executor.snapshotTerminalDecisionPolicy(context.Background(), &identityBoundTurn{})
	if err != nil {
		t.Fatalf("snapshot policy: %v", err)
	}
	if !enabled {
		t.Fatal("provider must remain enabled when no secure-session scope is available")
	}
	if policy.Revision != "0" {
		t.Fatalf("generation-default policy revision = %q, want 0", policy.Revision)
	}
}

func TestSnapshotTerminalDecisionPolicyUsesSecureScopeOverride(t *testing.T) {
	provider := admissionProviderStub{}
	store := terminaldecisionpolicy.NewStore(terminaldecisionpolicy.Config{})
	turn := secureAdmissionTurn()
	key := terminaldecisionpolicy.Key{
		SecureSessionIncarnation: string(turn.secureTurn.SessionID),
		ALegID:                   turn.aLeg.ALegID,
		FeatureID:                terminalDecisionFeatureID,
	}
	authority := terminaldecisionpolicy.Authority{
		SecureSessionIncarnation: key.SecureSessionIncarnation,
		ALegID:                   key.ALegID,
		Authorized:               true,
	}
	if _, err := store.Set(context.Background(), authority, key, terminaldecisionpolicy.ActorOperator, terminaldecisionpolicy.TriStateDisabled); err != nil {
		t.Fatalf("set secure override: %v", err)
	}
	executor := admissionExecutor(provider)
	executor.TerminalDecisionPolicy = store

	policy, enabled, err := executor.snapshotTerminalDecisionPolicy(context.Background(), turn)
	if err != nil {
		t.Fatalf("snapshot policy: %v", err)
	}
	if enabled {
		t.Fatal("secure-session disable override must disable the provider")
	}
	if policy.Revision == "0" {
		t.Fatal("secure-session override must carry the store revision")
	}
}

func TestSnapshotTerminalDecisionPolicyWithoutProviderIsDisabled(t *testing.T) {
	executor := admissionExecutor(nil)

	_, enabled, err := executor.snapshotTerminalDecisionPolicy(context.Background(), &identityBoundTurn{})
	if err != nil {
		t.Fatalf("snapshot policy: %v", err)
	}
	if enabled {
		t.Fatal("nil provider must disable terminal-decision evaluation")
	}
}

type admissionProviderStub struct{}

func (admissionProviderStub) ID() string { return "admission-test-provider" }

func (admissionProviderStub) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

func admissionExecutor(provider terminaldecision.Provider) *Executor {
	return &Executor{
		ExtensionRuntime: ExtensionRuntime{
			RuntimeSnapshot: extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
				FeaturePlanes: freezeBundle(lipfeature.FeatureBundle{
					SchemaVersion:            lipfeature.SchemaVersionV1,
					TerminalDecisionProvider: provider,
				}),
			}),
		},
	}
}

func secureAdmissionTurn() *identityBoundTurn {
	return &identityBoundTurn{
		aLeg: b2bua.ALegRecord{ALegID: "a-leg-admission"},
		secureTurn: execctx.SecureSessionTurn{
			SessionID: domain.SessionID("session-admission"),
			TurnID:    domain.TurnID("turn-admission"),
		},
		secureTurnOK: true,
	}
}
