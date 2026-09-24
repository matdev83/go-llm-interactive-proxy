package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPrepareRecvEventUsesHostOnlyV2AsAuthoritativeEconomicPath(t *testing.T) {
	stream := &phase7EconomicRuntimeStream{observations: []metering.Observation{phase7EconomicRuntimeObservation()}}
	attempt := &attemptSession{inner: stream}
	pipeline := newResponsePipeline()
	start := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     11,
		CacheReadTokens: 3,
		UsagePresence:   lipapi.UsagePresence{InputTokens: true, CacheReadTokens: true, OutputTokens: true},
		Accounting:      lipapi.UsageAccountingMetadata{DedupeKey: "anthropic.messages:stream"},
	}
	delta := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		OutputTokens:  8,
		UsagePresence: lipapi.UsagePresence{OutputTokens: true},
		Accounting:    start.Accounting,
	}
	for _, event := range []lipapi.Event{start, delta} {
		prepared := pipeline.prepareRecvEvent(context.Background(), recvTurnFacts{}, attempt, event)
		if prepared.swallowed {
			t.Fatalf("canonical event was swallowed before host-only V2 drain: %#v", event)
		}
	}
	if evidence, conflicts := attempt.billingEvidenceDrain(); len(evidence) != 0 || len(conflicts) != 0 {
		t.Fatalf("canonical V1 evidence=%d conflicts=%d, want no parallel durable path", len(evidence), len(conflicts))
	}
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)
	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 || len(conflicts) != 0 {
		t.Fatalf("host-only V2 evidence=%d conflicts=%d, want one authoritative observation", len(economic), len(conflicts))
	}
}

func TestPrepareRecvEventUsesConnectorSidebandAsAuthoritativeV1Path(t *testing.T) {
	complete := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     11,
		OutputTokens:    8,
		CacheReadTokens: 3,
		UsagePresence:   lipapi.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
			DedupeKey: "connector.anthropic:stream",
		},
	}
	stream := &usageSidebandStream{
		evidence: []lipapi.Event{complete},
		event:    lipapi.Event{Kind: lipapi.EventResponseFinished},
	}
	attempt := &attemptSession{inner: stream}
	pipeline := newResponsePipeline()
	partial := complete
	partial.OutputTokens = 0
	partial.UsagePresence.OutputTokens = true
	for _, event := range []lipapi.Event{partial, complete} {
		prepared := pipeline.prepareRecvEvent(context.Background(), recvTurnFacts{}, attempt, event)
		if prepared.swallowed {
			t.Fatalf("canonical connector event was swallowed before sideband drain: %#v", event)
		}
	}
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)
	evidence, conflicts := attempt.billingEvidenceDrain()
	if len(evidence) != 1 || len(conflicts) != 0 {
		t.Fatalf("connector V1 evidence=%d conflicts=%d, want one authoritative complete record", len(evidence), len(conflicts))
	}
	got := evidence[0].event
	if got.InputTokens != 11 || got.OutputTokens != 8 || got.CacheReadTokens != 3 {
		t.Fatalf("connector sideband counters=%+v, want input=11 output=8 cache=3", got)
	}
}

type auxiliaryV1UsageSidebandStream struct {
	usageSidebandStream
}

// Codex compaction is native maintenance evidence. It is additive to the
// wrapped response request and therefore must not suppress the primary
// canonical usage event at the runtime boundary.
func (*auxiliaryV1UsageSidebandStream) AccountingEvidenceEnabled() bool { return false }

func TestPrepareRecvEventKeepsAuxiliaryV1AlongsideCanonicalPrimary(t *testing.T) {
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	primary := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   11,
		OutputTokens:  8,
		TotalTokens:   19,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "codex.responses:response-1",
		},
	}
	auxiliary := primary
	auxiliary.InputTokens = 5
	auxiliary.OutputTokens = 2
	auxiliary.TotalTokens = 7
	auxiliary.Accounting.DedupeKey = "codex-compaction:conversation-1"
	stream := &auxiliaryV1UsageSidebandStream{usageSidebandStream: usageSidebandStream{
		evidence: []lipapi.Event{auxiliary},
		event:    lipapi.Event{Kind: lipapi.EventResponseFinished},
		err:      context.Canceled,
	}}
	attempt := &attemptSession{inner: stream, billingStoreID: "store-phase8"}
	pipeline := newResponsePipeline()
	prepared := pipeline.prepareRecvEvent(context.Background(), recvTurnFacts{}, attempt, primary)
	if prepared.swallowed {
		t.Fatal("primary canonical usage was swallowed by auxiliary V1 evidence")
	}
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)
	evidence, conflicts := attempt.billingEvidenceDrain()
	if len(evidence) != 2 || len(conflicts) != 0 {
		t.Fatalf("captured evidence=%d conflicts=%d, want primary plus auxiliary", len(evidence), len(conflicts))
	}
	record := billingLegRecord(billingLegDraft{
		callID: callID, aLegID: "a-phase8", storeID: "store-phase8", bLegID: "b-phase8", seq: 1,
		primary: routing.Primary{Backend: "codex", Model: "model"}, evidenceEvents: evidence,
	})
	if len(record.Observations) != 2 {
		t.Fatalf("durable observations=%d, want primary plus auxiliary", len(record.Observations))
	}
	keys := map[string]bool{}
	for _, observation := range record.Observations {
		keys[observation.SourceEventKey] = true
	}
	if !keys[primary.Accounting.DedupeKey] || !keys[auxiliary.Accounting.DedupeKey] {
		t.Fatalf("durable source keys=%v, want %q and %q", keys, primary.Accounting.DedupeKey, auxiliary.Accounting.DedupeKey)
	}
}

func TestBillingRecordDoesNotDuplicateCanonicalFallbackCoveredByV2(t *testing.T) {
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	economic := phase7EconomicRuntimeObservation()
	stream := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   2,
		UsagePresence: lipapi.UsagePresence{InputTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
			DedupeKey: economic.SourceEventKey,
		},
	}
	record := billingLegRecord(billingLegDraft{
		callID: callID, aLegID: "a-phase8", storeID: "store-phase7", bLegID: "b-phase7", seq: 1,
		primary:              routing.Primary{Backend: "backend", Model: "model"},
		stream:               stream,
		economicObservations: []execbackend.EconomicEvidence{{Observation: economic}},
	})
	if len(record.Observations) != 1 {
		t.Fatalf("record observations=%d, want one authoritative V2 observation", len(record.Observations))
	}
	if record.Observations[0].StreamID != economic.StreamID || record.Observations[0].SourceEventKey != economic.SourceEventKey {
		t.Fatalf("record retained canonical fallback instead of V2 source: %+v", record.Observations[0])
	}
}

func TestBillingRecordDoesNotDuplicateV1SidebandCoveredByV2(t *testing.T) {
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	economic := phase7EconomicRuntimeObservation()
	v1 := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   2,
		UsagePresence: lipapi.UsagePresence{InputTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
			DedupeKey: economic.SourceEventKey,
		},
	}
	record := billingLegRecord(billingLegDraft{
		callID: callID, aLegID: "a-phase8", storeID: "store-phase7", bLegID: "b-phase7", seq: 1,
		primary:              routing.Primary{Backend: "backend", Model: "model"},
		evidenceEvents:       []capturedBillingEvidence{{event: v1, role: billingEvidenceRoleSideband}},
		economicObservations: []execbackend.EconomicEvidence{{Observation: economic}},
	})
	if len(record.Observations) != 1 {
		t.Fatalf("record observations=%d, want one authoritative V2 observation", len(record.Observations))
	}
	if record.Observations[0].StreamID != economic.StreamID || record.Observations[0].SourceEventKey != economic.SourceEventKey {
		t.Fatalf("record retained V1 sideband instead of V2 source: %+v", record.Observations[0])
	}
}

func TestAuthorityUsageEventDoesNotDoubleCountCumulativeProviderSnapshots(t *testing.T) {
	accounting := lipapi.UsageAccountingMetadata{
		Plane:     lipapi.UsagePlaneProviderBillable,
		Source:    lipapi.UsageSourceProviderReported,
		Authority: lipapi.UsageAuthorityAuthoritative,
		DedupeKey: "anthropic.messages:stream",
	}
	start := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     11,
		CacheReadTokens: 3,
		UsagePresence:   lipapi.UsagePresence{InputTokens: true, CacheReadTokens: true, OutputTokens: true},
		Accounting:      accounting,
	}
	delta := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		OutputTokens:  8,
		UsagePresence: lipapi.UsagePresence{OutputTokens: true},
		Accounting:    accounting,
	}
	repeated := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     11,
		OutputTokens:    8,
		CacheReadTokens: 3,
		UsagePresence:   lipapi.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true},
		Accounting:      accounting,
	}
	got := authorityUsageEvent([]lipapi.Event{start, delta, repeated})
	if got.InputTokens != 11 || got.OutputTokens != 8 || got.CacheReadTokens != 3 {
		t.Fatalf("cumulative provider usage=%+v, want input=11 output=8 cache=3", got)
	}
}
