package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase7Adapter_V2SidebandDrainDeduplicatesFrameReplay(t *testing.T) {
	t.Parallel()
	s := &managedStream{
		ctx:      contextBackground(),
		opt:      Options{Negotiation: phase7Negotiation()},
		maxFrame: int(backendplugin.DefaultMaxStreamFrameBytes),
	}
	evidence := backendplugin.AccountingEvidenceV2{Observation: phase7AdapterObservation(), Coverage: backendplugin.EvidenceCoverageComplete}
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		t.Fatalf("accepted frame: %v", err)
	}
	frame := backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: 1, AccountingV2: &evidence}
	if err := s.onPluginFrame(frame); err != nil {
		t.Fatalf("first V2 frame: %v", err)
	}
	frame.Sequence = 2
	if err := s.onPluginFrame(frame); err != nil {
		t.Fatalf("duplicate V2 frame: %v", err)
	}
	got := s.DrainAccountingEvidenceV2()
	if len(got) != 1 {
		t.Fatalf("drained %d observations, want one replay-safe provider charge", len(got))
	}
	if got[0].Observation.Correlation.ProviderRequestID != "provider-request-1" {
		t.Fatalf("provider identity lost: %+v", got[0].Observation.Correlation)
	}
}

func TestPhase7Adapter_V2ConflictIsVisibleAndFailClosed(t *testing.T) {
	t.Parallel()
	s := &managedStream{ctx: contextBackground(), opt: Options{Negotiation: phase7Negotiation()}, maxFrame: int(backendplugin.DefaultMaxStreamFrameBytes)}
	first := backendplugin.AccountingEvidenceV2{Observation: phase7AdapterObservation(), Coverage: backendplugin.EvidenceCoverageComplete}
	second := first
	second.Observation.Measures = append([]metering.Measure(nil), first.Observation.Measures...)
	second.Observation.Measures[0].Value = &metering.Decimal{Coefficient: "3", Scale: 0}
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		t.Fatalf("accepted frame: %v", err)
	}
	for i, evidence := range []*backendplugin.AccountingEvidenceV2{&first, &second} {
		err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: uint64(i + 1), AccountingV2: evidence})
		if i == 0 && err != nil {
			t.Fatalf("first frame: %v", err)
		}
		if i == 1 && !errors.Is(err, backendplugin.ErrAccountingEvidenceV2Conflict) {
			t.Fatalf("conflicting frame error=%v, want conflict", err)
		}
	}
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: 3, AccountingV2: &second}); !errors.Is(err, backendplugin.ErrAccountingEvidenceV2Conflict) {
		t.Fatalf("replayed conflicting frame error=%v, want conflict", err)
	}
	if got := s.DrainAccountingEvidenceV2(); len(got) != 2 {
		t.Fatalf("conflict payload was not retained for reconciliation: %d", len(got))
	}
}

func TestPhase7Adapter_V2ConflictIncludesCoverageDispositionAndRetainsCopy(t *testing.T) {
	t.Parallel()
	s := &managedStream{ctx: contextBackground(), opt: Options{Negotiation: phase7Negotiation()}, maxFrame: int(backendplugin.DefaultMaxStreamFrameBytes)}
	evidence := backendplugin.AccountingEvidenceV2{Observation: phase7AdapterObservation()}
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		t.Fatalf("accepted frame: %v", err)
	}
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: 1, AccountingV2: &evidence}); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	// Mutating the caller-owned frame must not mutate the retained provider
	// evidence. The replay below is intentionally a coverage conflict, not a
	// changed observation payload.
	evidence.Coverage = backendplugin.EvidenceCoveragePartial
	evidence.CoverageReason = "legacy subset"
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: 2, AccountingV2: &evidence}); !errors.Is(err, backendplugin.ErrAccountingEvidenceV2Conflict) {
		t.Fatalf("coverage conflict error=%v, want conflict", err)
	}
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: 3, AccountingV2: &evidence}); !errors.Is(err, backendplugin.ErrAccountingEvidenceV2Conflict) {
		t.Fatalf("coverage conflict replay error=%v, want conflict", err)
	}
	got := s.DrainAccountingEvidenceV2()
	if len(got) != 2 || got[0].Coverage != backendplugin.EvidenceCoverageComplete || got[0].Observation.Measures[0].Value.Coefficient != "2" {
		t.Fatalf("retained evidence=%+v, want copied complete original plus conflict", got)
	}
}

func phase7Negotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidenceV2, EnabledFeatures: []string{backendplugin.FeatureAccountingEvidenceV2}}
}

func phase7AdapterObservation() metering.Observation {
	now := time.Unix(1700000000, 0).UTC()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "provider.media.v2"}
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-adapter-1", SourceEventKey: "provider-event-1", Revision: 1,
		StreamID: "stream-adapter", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "metering", BLegID: "b-adapter", AttemptID: "attempt-adapter"},
		Correlation: metering.CorrelationV2{StoreID: "metering", BLegID: "b-adapter", AttemptID: "attempt-adapter", ProviderRequestID: "provider-request-1", ProviderAccountKey: "acct-1"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider-schema-v2",
		Measures: []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
	}
}

// contextBackground keeps this package's test fixtures independent from the
// adapter's production context ownership while remaining non-nil.
func contextBackground() context.Context { return context.Background() }

var _ lipapi.ManagedEventStream = (*managedStream)(nil)
