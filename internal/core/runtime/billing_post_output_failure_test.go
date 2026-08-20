package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type mockFailingAppender struct {
	callAppends atomic.Int32
	legAppends  atomic.Int32
}

func (m *mockFailingAppender) AppendCall(ctx context.Context, record billing.CallUsageRecord) error {
	m.callAppends.Add(1)
	return errors.New("failing call usage append")
}

func (m *mockFailingAppender) AppendLeg(ctx context.Context, record billing.CallLegUsageRecord) error {
	m.legAppends.Add(1)
	return errors.New("failing leg usage append")
}

func TestBillingAppendRetryOutputPersistenceFailureAfterSuccess(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var opens atomic.Int32
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(3)

	// Set up the failing terminal sink.
	appender := &mockFailingAppender{}
	ex.TerminalUsageSink = appender

	// Setup Billing Identity
	ex.BillingIdentity = BillingIdentity{
		AccountID: func(context.Context, lipapi.Call) string { return "acct-test" },
		CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "pricing:test", Version: "1"}
		},
		ChargePolicyRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "policy:test", Version: "1"}
		},
		OperatorRateRef: func(context.Context, string, string) billing.VersionRef {
			return billing.VersionRef{ID: "operator:test", Version: "1"}
		},
	}
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{
			AccountID:       "acct-test",
			CallID:          in.CallID,
			PricingRef:      billing.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: billing.VersionRef{ID: "policy:test", Version: "1"},
			Status:          billing.ExposureOpen,
		}, nil
	})

	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "ok"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}

	col, err := lipapi.Collect(context.Background(), stream)
	// Output/client-visible stream must succeed despite database append failures
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text: %q, want 'ok'", col.Text.String())
	}

	// Verify that only 1 backend open happened (no retry/failover triggered by database failure)
	if got := opens.Load(); got != 1 {
		t.Fatalf("backend opens = %d, want 1 (no retry/failover occurred)", got)
	}
	if got := appender.callAppends.Load(); got == 0 {
		t.Fatal("AppendCall was not attempted")
	}
	if got := appender.legAppends.Load(); got == 0 {
		t.Fatal("AppendLeg was not attempted")
	}
}
