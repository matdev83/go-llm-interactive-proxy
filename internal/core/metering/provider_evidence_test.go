package metering

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestProviderEvidenceBufferRequiresTrustedBLegBinding(t *testing.T) {
	b := NewProviderEvidenceBuffer()
	b.AddUsageEvent(providerUsageEvent(3, 2), "test.provider.v2")
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("unbound provider evidence must not become an observation: %d", len(got))
	}
	b.BindEconomicEvidence(testObservationIdentity())
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("bound provider evidence observations = %d, want 1", len(observations))
	}
	if got := observations[0].Subject.Kind; got != sdkmetering.SubjectBLeg {
		t.Fatalf("subject kind = %q, want B-leg", got)
	}
	if got := observations[0].Subject.BLegID; got != "b-leg-1" {
		t.Fatalf("B-leg = %q, want b-leg-1", got)
	}
}

func TestProviderEvidenceBufferDeduplicatesReplayAndRetainsRevision(t *testing.T) {
	b := NewProviderEvidenceBuffer()
	b.AddUsageEvent(providerUsageEvent(3, 2), "test.provider.v2")
	b.AddUsageEvent(providerUsageEvent(3, 2), "test.provider.v2")
	b.BindEconomicEvidence(testObservationIdentity())
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("replay observations = %d, want 1", len(observations))
	}
	if observations[0].Revision != 1 {
		t.Fatalf("initial revision = %d, want 1", observations[0].Revision)
	}
	changed := providerUsageEvent(4, 2)
	b.AddUsageEvent(changed, "test.provider.v2")
	changedObservations := b.DrainEconomicObservations()
	if len(changedObservations) != 1 {
		t.Fatalf("late revision observations = %d, want 1", len(changedObservations))
	}
	if changedObservations[0].Revision != 2 {
		t.Fatalf("late revision = %d, want 2", changedObservations[0].Revision)
	}
	if observations[0].Fingerprint() == changedObservations[0].Fingerprint() {
		t.Fatal("changed provider payload must retain a distinct fingerprint")
	}
	if len(changedObservations[0].Supersedes) != 1 || changedObservations[0].Supersedes[0].ObservationID != observations[0].ID {
		t.Fatalf("revision supersession = %+v, want prior observation %q", changedObservations[0].Supersedes, observations[0].ID)
	}
	// Exact replay remains suppressed after the first drain as well.
	b.AddUsageEvent(changed, "test.provider.v2")
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("replayed late revision observations = %d, want 0", len(got))
	}
}

func TestProviderEvidenceBufferRetainsABACorrectionAcrossDrains(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	for _, wantInput := range []int{3, 4, 3} {
		b.AddUsageEvent(providerUsageEvent(wantInput, 2), "test.provider.v2")
		observations := b.DrainEconomicObservations()
		if len(observations) != 1 {
			t.Fatalf("input=%d observations=%d, want one correction", wantInput, len(observations))
		}
	}
	// Only an immediately consecutive replay is suppressed. The last A is
	// already the current source revision, so replaying it is a no-op.
	b.AddUsageEvent(providerUsageEvent(3, 2), "test.provider.v2")
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("consecutive A replay observations=%d, want 0", len(got))
	}
}

func TestProviderEvidenceBufferFingerprintRetainsSemanticChanges(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	base := ProviderEvidenceDraft{
		SourceEventKey: "provider.semantic",
		StreamID:       "provider.v2",
		Origin:         sdkmetering.OriginProvider,
		Acquisition:    sdkmetering.AcquisitionProviderResponse,
		Authority:      sdkmetering.AuthorityObservedClaim,
		Perspective:    sdkmetering.PerspectiveOperator,
		Boundary:       sdkmetering.BoundaryBackendEgress,
		Lifecycle:      sdkmetering.LifecycleBackendAttempt,
		Semantics:      sdkmetering.SemanticsCumulative,
		Coverage:       "partial",
		CoverageReason: "start snapshot",
	}
	changed := base
	changed.Origin = sdkmetering.OriginLocal
	changed.Acquisition = sdkmetering.AcquisitionLocalEstimator
	changed.Authority = sdkmetering.AuthorityEstimatedClaim
	changed.Perspective = sdkmetering.PerspectiveCustomer
	changed.Boundary = sdkmetering.BoundaryFrontendIngress
	changed.Lifecycle = sdkmetering.LifecycleLogicalRequest
	changed.Semantics = sdkmetering.SemanticsCorrection
	changed.Coverage = "complete"
	changed.CoverageReason = "corrected snapshot"
	b.Add(base)
	b.Add(changed)
	b.mu.Lock()
	got := len(b.drafts)
	b.mu.Unlock()
	if got != 2 {
		t.Fatalf("semantic revisions retained=%d, want 2", got)
	}
}

func TestProviderEvidenceBufferIgnoresTimestampOnlyReplay(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	usage := providerUsageEvent(3, 2)
	first := ProviderEvidenceDraft{
		SourceEventKey: "provider.timestamp", StreamID: "provider.v2",
		Measures: providerTokenMeasures(usage, "provider.v2"), Evidence: providerTokenEvidence(usage),
		ObservedAt: time.Unix(10, 0), ReceivedAt: time.Unix(11, 0),
	}
	second := first
	second.ObservedAt = time.Unix(20, 0)
	second.ReceivedAt = time.Unix(21, 0)
	b.Add(first)
	if got := b.DrainEconomicObservations(); len(got) != 1 {
		t.Fatalf("initial timestamp payload observations=%d, want 1", len(got))
	}
	b.Add(second)
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("timestamp-only replay observations=%d, want 0", len(got))
	}
}

func TestProviderEvidenceBufferRetainsDistinctExplicitSourceRevisions(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	usage := providerUsageEvent(3, 2)
	draft := ProviderEvidenceDraft{
		SourceEventKey: "provider.explicit-revision", StreamID: "provider.v2", Revision: 1,
		Measures: providerTokenMeasures(usage, "provider.v2"), Evidence: providerTokenEvidence(usage),
	}
	b.Add(draft)
	revisionTwo := draft
	revisionTwo.Revision = 2
	b.Add(revisionTwo)
	observations := b.DrainEconomicObservations()
	if len(observations) != 2 {
		t.Fatalf("explicit source revisions observations=%d, want 2", len(observations))
	}
	if observations[0].Revision != 1 || observations[1].Revision != 2 {
		t.Fatalf("explicit revisions=%d,%d, want 1,2", observations[0].Revision, observations[1].Revision)
	}
	// A replay of the older explicit source revision is still a replay even
	// after a later revision has been accepted.
	b.Add(draft)
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("replayed explicit source revision observations=%d, want 0", len(got))
	}
}

func TestProviderEvidenceBufferRejectsConflictingExplicitSourceRevision(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	first := ProviderEvidenceDraft{
		SourceEventKey: "provider.explicit-conflict", StreamID: "provider.v2", Revision: 7,
		Measures: providerTokenMeasures(providerUsageEvent(3, 2), "provider.v2"),
	}
	b.Add(first)
	if got := b.DrainEconomicObservations(); len(got) != 1 {
		t.Fatalf("initial explicit source revision observations=%d, want 1", len(got))
	}
	conflict := first
	conflict.Measures = providerTokenMeasures(providerUsageEvent(4, 2), "provider.v2")
	b.Add(conflict)
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("conflicting explicit source revision observations=%d, want 0", len(got))
	}
	// Retrying the conflicting payload remains closed rather than becoming a
	// new immutable revision or repeatedly producing the same conflict.
	b.Add(conflict)
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("retried conflicting explicit source revision observations=%d, want 0", len(got))
	}
}

func TestProviderEvidenceBufferPreservesMediaChargeAndProviderLineage(t *testing.T) {
	b := NewProviderEvidenceBuffer()
	amount := sdkmetering.Decimal{Coefficient: "0"}
	b.Add(ProviderEvidenceDraft{
		SourceEventKey:     "provider.response:1",
		StreamID:           "test.provider.v2",
		ProviderAccountKey: "acct-1",
		ProviderRequestID:  "req-1",
		ProviderChargeID:   "charge-1",
		Measures: []sdkmetering.Measure{
			{Key: sdkmetering.ComponentKey{Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentImage, Unit: sdkmetering.UnitImage, SchemaID: "provider.test.v1"}, Value: decimalPtr("2"), Quality: sdkmetering.QualityObserved},
			{Key: sdkmetering.ComponentKey{Direction: sdkmetering.DirectionOutput, Component: sdkmetering.ComponentAudio, Unit: sdkmetering.UnitSecond, SchemaID: "provider.test.v1"}, Value: &sdkmetering.Decimal{Coefficient: "15", Scale: 1}, Quality: sdkmetering.QualityObserved},
		},
		Charges:  []sdkmetering.ReportedCharge{{ChargeItemID: "charge-1", Amount: &amount, Currency: "USD", Kind: sdkmetering.ChargeKindAggregate}},
		Evidence: []sdkmetering.SafeEvidenceField{{Path: "$.cost.amount", Lexeme: "0", Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse}},
	})
	b.mu.Lock()
	draft := cloneProviderEvidenceDraft(b.drafts[0])
	b.mu.Unlock()
	if _, err := providerObservationValidated(testObservationIdentity(), draft); err != nil {
		t.Fatalf("media draft should validate: %v", err)
	}
	b.BindEconomicEvidence(testObservationIdentity())
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("media provider observation count = %d, want 1", len(observations))
	}
	o := observations[0]
	if o.Subject.ProviderAccountKey != "acct-1" || o.Correlation.ProviderRequestID != "req-1" || o.Subject.ProviderChargeID != "charge-1" {
		t.Fatalf("provider lineage was not preserved: subject=%+v correlation=%+v", o.Subject, o.Correlation)
	}
	if len(o.Measures) != 2 || len(o.Charges) != 1 {
		t.Fatalf("media/charge counts = %d/%d, want 2/1", len(o.Measures), len(o.Charges))
	}
	if o.Charges[0].Amount == nil || o.Charges[0].Amount.Coefficient != "0" {
		t.Fatalf("present zero charge was not preserved: %+v", o.Charges[0])
	}
}

func TestProviderEvidenceBufferRejectsUnsafeEvidenceWithoutDroppingOtherDrafts(t *testing.T) {
	b := NewProviderEvidenceBuffer()
	b.Add(ProviderEvidenceDraft{
		SourceEventKey: "unsafe",
		Evidence:       []sdkmetering.SafeEvidenceField{{Path: "$.secret", Lexeme: "should-not-retain", Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse}},
	})
	b.AddUsageEvent(providerUsageEvent(1, 1), "test.provider.v2")
	b.BindEconomicEvidence(testObservationIdentity())
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("unsafe draft should be suppressed while valid draft remains: %d", len(observations))
	}
	if observations[0].SourceEventKey != "test.provider.v2:stream" {
		t.Fatalf("remaining source key = %q", observations[0].SourceEventKey)
	}
}

func providerUsageEvent(input, output int) lipapi.Event {
	return lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: input, OutputTokens: output,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
			DedupeKey: "test.provider.v2:stream", ProviderAccountKey: "acct", ProviderRequestID: "req",
		},
	}
}

func testObservationIdentity() ObservationIdentity {
	now := time.Unix(1700000000, 0).UTC()
	return ObservationIdentity{
		StoreID: "store-1", RequestID: "request-1", CallID: "call-1", BillingCallID: "billing-1",
		ALegID: "a-leg-1", BLegID: "b-leg-1", AttemptID: "attempt-1", AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	}
}

func decimalPtr(coefficient string) *sdkmetering.Decimal {
	value := sdkmetering.Decimal{Coefficient: coefficient}
	return &value
}
