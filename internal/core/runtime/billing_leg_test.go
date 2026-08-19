package runtime

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func TestBillingLegHandoffIsTerminalOnlyAndIdempotent(t *testing.T) {
	var mu sync.Mutex
	var records []billing.CallLegUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record billing.CallLegUsageRecord) {
			mu.Lock()
			records = append(records, record)
			mu.Unlock()
		}),
	}}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-2", ALegID: "a-1", Seq: 2}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}}, authorityLifecycle{}),
		lastAuthorityUsage: lipapi.Event{
			Kind:          lipapi.EventUsageDelta,
			InputTokens:   12,
			OutputTokens:  0,
			CostNanoUnits: 0,
			Currency:      "USD",
			CostPresent:   true,
			UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
			Accounting: lipapi.UsageAccountingMetadata{
				Source:    lipapi.UsageSourceProviderReported,
				Authority: lipapi.UsageAuthorityAuthoritative,
				DedupeKey: "provider-charge-2",
			},
		},
	}

	first := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil)
	if first.Err != nil {
		t.Fatalf("first terminalization: %v", first.Err)
	}
	second := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil)
	if second.Err != nil {
		t.Fatalf("repeated terminalization: %v", second.Err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("shadow records = %d, want exactly one", len(records))
	}
	got := records[0]
	if got.ALegID != "a-1" || got.BLegID != "b-2" || got.AttemptSeq != 2 {
		t.Fatalf("lineage = %+v", got)
	}
	if got.BackendID != "backend-b" || got.ProviderID != "backend-b" || got.ModelID != "model-b" {
		t.Fatalf("provider attribution = %+v", got)
	}
	if got.Outcome != billing.LegOutcomeWinner || got.Surfaced != billing.SurfacedYes {
		t.Fatalf("terminal state = %q/%q", got.Outcome, got.Surfaced)
	}
	if !got.Evidence.Cost.Present || got.Evidence.Cost.NanoUnits != 0 {
		t.Fatalf("explicit zero provider cost was lost: %+v", got.Evidence)
	}
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 12 || !got.Evidence.OutputTokens.Present || got.Evidence.OutputTokens.Value != 0 {
		t.Fatalf("usage presence/value = %+v", got.Evidence)
	}
}

func TestBillingLegObserverPanicCannotChangeTerminalResult(t *testing.T) {
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingLegObserver: BillingLegObserverFunc(func(context.Context, billing.CallLegUsageRecord) {
			panic("observer must be isolated")
		}),
	}}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}}, authorityLifecycle{}),
	}
	result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil)
	if result.Err != nil {
		t.Fatalf("observer panic changed terminal result: %v", result.Err)
	}
}

func TestBillingLegHandoffCoversSequentialReplacementBLegs(t *testing.T) {
	var records []billing.CallLegUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record billing.CallLegUsageRecord) {
			records = append(records, record)
		}),
	}}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}}, authorityLifecycle{}),
		lastAuthorityUsage: lipapi.Event{
			Kind: lipapi.EventUsageDelta, InputTokens: 9,
			UsagePresence: lipapi.UsagePresence{InputTokens: true},
		},
	}
	if result := stream.runAttemptTerminal(context.Background(), sdkterminal.CommandSwallowedAttempt, nil); result.Err != nil {
		t.Fatalf("first attempt terminalization: %v", result.Err)
	}
	stream.attempt.install(newAttemptSession(attemptSessionInput{bleg: b2bua.BLegRecord{BLegID: "b-2", ALegID: "a-1", Seq: 2}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}}}))
	stream.lastAuthorityUsage = lipapi.Event{}
	if result := stream.runAttemptTerminal(context.Background(), sdkterminal.CommandSwallowedAttempt, nil); result.Err != nil {
		t.Fatalf("replacement attempt terminalization: %v", result.Err)
	}
	if len(records) != 2 || records[0].BLegID != "b-1" || records[1].BLegID != "b-2" {
		t.Fatalf("shadow replacement records = %+v", records)
	}
	if records[1].Evidence.InputTokens.Present {
		t.Fatal("replacement LUR inherited prior B-leg evidence")
	}
}

func TestBillingLegUsesFinalizeBillingWhenSupported(t *testing.T) {
	var finalizeCalls int
	var records []billing.CallLegUsageRecord
	executor := &Executor{
		CoreRuntime: CoreRuntime{
			Backends: map[string]execbackend.Backend{
				"backend-b": {
					FinalizeBilling: func(_ context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
						finalizeCalls++
						if in.ALegID != "a-1" || in.BLegID != "b-2" {
							t.Fatalf("finalize lineage = %+v", in)
						}
						return lipapi.Event{
							Kind:          lipapi.EventUsageDelta,
							InputTokens:   99,
							OutputTokens:  7,
							UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
							Accounting: lipapi.UsageAccountingMetadata{
								Source:    lipapi.UsageSourceProviderReported,
								Authority: lipapi.UsageAuthorityAuthoritative,
								DedupeKey: "finalize-key",
							},
						}, nil
					},
				},
			},
		},
		BillingRuntime: BillingRuntime{
			BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record billing.CallLegUsageRecord) {
				records = append(records, record)
			}),
		},
	}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-2", ALegID: "a-1", Seq: 2}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}}, authorityLifecycle{}),
		lastAuthorityUsage: lipapi.Event{
			Kind:          lipapi.EventUsageDelta,
			InputTokens:   12,
			UsagePresence: lipapi.UsagePresence{InputTokens: true},
		},
	}
	if result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); result.Err != nil {
		t.Fatalf("terminalization: %v", result.Err)
	}
	if finalizeCalls != 1 {
		t.Fatalf("FinalizeBilling calls = %d, want 1", finalizeCalls)
	}
	if len(records) != 1 {
		t.Fatalf("shadow records = %d", len(records))
	}
	got := records[0].Evidence
	if !got.InputTokens.Present || got.InputTokens.Value != 99 || !got.OutputTokens.Present || got.OutputTokens.Value != 7 {
		t.Fatalf("shadow evidence ignored FinalizeBilling: %+v", got)
	}
	if got.DedupeKey != "finalize-key" {
		t.Fatalf("dedupe key = %q", got.DedupeKey)
	}
}

func TestBillingLegPreservesStreamAuthoritativeZeroCostAcrossFinalize(t *testing.T) {
	var records []billing.CallLegUsageRecord
	executor := &Executor{
		CoreRuntime: CoreRuntime{
			Backends: map[string]execbackend.Backend{
				"backend-b": {
					FinalizeBilling: func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error) {
						// FinalizeBilling ABI carries tokens/provenance only — no cost fields.
						return lipapi.Event{
							Kind:          lipapi.EventUsageDelta,
							InputTokens:   99,
							OutputTokens:  7,
							UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
							Accounting: lipapi.UsageAccountingMetadata{
								Source:    lipapi.UsageSourceProviderReported,
								DedupeKey: "finalize-key",
							},
						}, nil
					},
				},
			},
		},
		BillingRuntime: BillingRuntime{
			BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record billing.CallLegUsageRecord) {
				records = append(records, record)
			}),
		},
	}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-2", ALegID: "a-1", Seq: 2}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}}, authorityLifecycle{}),
		lastAuthorityUsage: lipapi.Event{
			Kind:          lipapi.EventUsageDelta,
			InputTokens:   12,
			CostNanoUnits: 0,
			Currency:      "USD",
			CostPresent:   true,
			UsagePresence: lipapi.UsagePresence{InputTokens: true},
			Accounting: lipapi.UsageAccountingMetadata{
				Source:    lipapi.UsageSourceProviderReported,
				Authority: lipapi.UsageAuthorityAuthoritative,
				DedupeKey: "stream-cost",
			},
		},
	}
	if result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); result.Err != nil {
		t.Fatalf("terminalization: %v", result.Err)
	}
	if len(records) != 1 {
		t.Fatalf("shadow records = %d", len(records))
	}
	got := records[0].Evidence
	if !got.InputTokens.Present || got.InputTokens.Value != 99 || !got.OutputTokens.Present || got.OutputTokens.Value != 7 {
		t.Fatalf("finalize tokens lost: %+v", got)
	}
	if !got.Cost.Present || got.Cost.NanoUnits != 0 || got.Cost.Currency != "USD" {
		t.Fatalf("stream authoritative zero cost was dropped by FinalizeBilling merge: %+v", got)
	}
	if got.Authority != billing.EvidenceAuthorityAuthoritative {
		t.Fatalf("stream authority was dropped with cost: %+v", got)
	}
	if got.DedupeKey != "finalize-key" {
		t.Fatalf("finalize provenance lost: %+v", got)
	}
}

func TestBillingLegFallsBackWhenFinalizeBillingFails(t *testing.T) {
	var records []billing.CallLegUsageRecord
	executor := &Executor{
		CoreRuntime: CoreRuntime{
			Backends: map[string]execbackend.Backend{
				"backend-b": {
					FinalizeBilling: func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error) {
						return lipapi.Event{}, errors.New("finalize unavailable")
					},
				},
			},
		},
		BillingRuntime: BillingRuntime{
			BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record billing.CallLegUsageRecord) {
				records = append(records, record)
			}),
		},
	}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-2", ALegID: "a-1", Seq: 2}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}}, authorityLifecycle{}),
		lastAuthorityUsage: lipapi.Event{
			Kind:          lipapi.EventUsageDelta,
			InputTokens:   12,
			UsagePresence: lipapi.UsagePresence{InputTokens: true},
		},
	}
	if result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); result.Err != nil {
		t.Fatalf("finalize failure changed terminal result: %v", result.Err)
	}
	if len(records) != 1 || !records[0].Evidence.InputTokens.Present || records[0].Evidence.InputTokens.Value != 12 {
		t.Fatalf("expected stream fallback evidence, got %+v", records)
	}
}

func TestBillingLegUsesDistinctProviderParamWhenPresent(t *testing.T) {
	var got billing.CallLegUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record billing.CallLegUsageRecord) {
			got = record
		}),
	}}
	params := make(url.Values)
	params.Set("provider", "openai")
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{
			Backend: "backend-azure", Model: "model-x", Params: params,
		}}, authorityLifecycle{}),
	}
	if result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); result.Err != nil {
		t.Fatal(result.Err)
	}
	if got.BackendID != "backend-azure" || got.ProviderID != "openai" {
		t.Fatalf("attribution BackendID=%q ProviderID=%q", got.BackendID, got.ProviderID)
	}
}

func TestBillingLegEmptyBLegIDUsesColonFreeSyntheticID(t *testing.T) {
	var got billing.CallLegUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record billing.CallLegUsageRecord) {
			got = record
		}),
	}}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "", ALegID: "a-1", Seq: 3}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}}, authorityLifecycle{}),
	}
	if result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); result.Err != nil {
		t.Fatal(result.Err)
	}
	if got.BLegID != "seq_3" {
		t.Fatalf("synthetic BLegID = %q, want seq_3 (colon-free for LURKey)", got.BLegID)
	}
	if strings.Contains(got.BLegID, ":") {
		t.Fatalf("synthetic BLegID contains reserved delimiter: %q", got.BLegID)
	}
}

func TestBillingLegFallbackUsesLastUsageDeltaNotCumulativeSum(t *testing.T) {
	var records []billing.CallLegUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingLegObserver: BillingLegObserverFunc(func(_ context.Context, record billing.CallLegUsageRecord) {
			records = append(records, record)
		}),
	}}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}}, authorityLifecycle{}),
		seenEvents: []lipapi.Event{
			{
				Kind:          lipapi.EventUsageDelta,
				InputTokens:   10,
				OutputTokens:  0,
				TotalTokens:   10,
				UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
			},
			{
				Kind:          lipapi.EventUsageDelta,
				InputTokens:   20,
				OutputTokens:  0,
				TotalTokens:   20,
				UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
			},
		},
	}
	if result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); result.Err != nil {
		t.Fatalf("terminalization: %v", result.Err)
	}
	if len(records) != 1 {
		t.Fatalf("shadow records = %d, want 1", len(records))
	}
	got := records[0].Evidence
	if !got.InputTokens.Present || got.InputTokens.Value != 20 || got.TotalTokens.Value != 20 {
		t.Fatalf("LUR fallback must use last cumulative delta 20, not sum 30: %+v", got)
	}
}

func TestFinalizeBillingOncePerBLegForQuotaAndLUR(t *testing.T) {
	var calls int
	executor := &Executor{
		CoreRuntime: CoreRuntime{
			Backends: map[string]execbackend.Backend{
				"backend-b": {
					FinalizeBilling: func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error) {
						calls++
						return lipapi.Event{
							Kind:          lipapi.EventUsageDelta,
							InputTokens:   5,
							UsagePresence: lipapi.UsagePresence{InputTokens: true},
						}, nil
					},
				},
			},
		},
		BillingRuntime: BillingRuntime{
			BillingLegObserver: BillingLegObserverFunc(func(context.Context, billing.CallLegUsageRecord) {}),
		},
	}
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: "a-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-2", ALegID: "a-1", Seq: 2}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}}, authorityLifecycle{}),
	}
	if !stream.finalizeBillingAfterCancel(context.Background(), "client canceled") {
		t.Fatal("quota finalize should succeed")
	}
	stream.recordBillingLeg(context.Background(), sdkterminal.CommandCancel)
	if calls != 1 {
		t.Fatalf("FinalizeBilling calls = %d, want 1 shared snapshot for quota and LUR", calls)
	}
}
