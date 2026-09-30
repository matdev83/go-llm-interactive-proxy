package runtimebundle

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// R10 adversarial-repair contract: appendLinkedStatementEvidence must resolve
// linked statement evidence through a selective indexed correlation/B-leg bound
// instead of paging every observation for a provider account. The number of
// ListObservations pages must not grow with unrelated provider-account history.

// bridgeListQueryRecorder counts the database-side ListObservations pages that
// the relay actually issues. Every SQL execution of the observation listing
// query is one page.
type bridgeListQueryRecorder struct {
	pages int
}

func (h *bridgeListQueryRecorder) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *bridgeListQueryRecorder) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if strings.Contains(event.Query, "FROM metering_facts f WHERE") {
		h.pages++
	}
}

func (h *bridgeListQueryRecorder) reset() { h.pages = 0 }

func TestObservationEconomicRelayLinkedStatementLookupStaysBoundedAsHistoryGrows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newBridgeMeteringStore(t, "bridge-store")
	source := bridgeRuntimeObservation("bounded-source", 1)
	statement := bridgeStatementObservation(t, "line-bounded", "b", "bridge-store", 1)

	require.NoError(t, store.AppendObservation(ctx, source))
	require.NoError(t, store.AppendObservation(ctx, statement))
	require.NoError(t, appendBridgeUnrelatedHistory(ctx, store, "provider", 0, 100))

	relay := &observationEconomicRelay{journal: store}
	recorder := &bridgeListQueryRecorder{}
	store.DB().AddQueryHook(recorder)

	evidence := make([]metering.Observation, 0)
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.True(t, bridgeEvidenceContains(evidence, statement.IdentityKey()), "linked statement must be discovered")
	small := recorder.pages

	// Grow unrelated provider-account history by an order of magnitude. A
	// bounded indexed lookup must not scan more pages.
	require.NoError(t, appendBridgeUnrelatedHistory(ctx, store, "provider", 100, 2000))
	recorder.reset()
	evidence = evidence[:0]
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.True(t, bridgeEvidenceContains(evidence, statement.IdentityKey()), "linked statement must remain discoverable")
	grown := recorder.pages

	require.Equal(t, small, grown,
		"linked-statement lookup pages must not grow with unrelated provider-account history (small=%d grown=%d)", small, grown)
	require.LessOrEqual(t, grown, 2, "linked-statement lookup must stay within a bounded page count, got %d", grown)
}

func TestObservationEconomicRelayLinkedStatementNoMatchIsQuiet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newBridgeMeteringStore(t, "bridge-store")
	source := bridgeRuntimeObservation("no-match-source", 1)
	require.NoError(t, store.AppendObservation(ctx, source))
	require.NoError(t, appendBridgeUnrelatedHistory(ctx, store, "provider", 0, 50))

	relay := &observationEconomicRelay{journal: store}
	evidence := make([]metering.Observation, 0)
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.Empty(t, evidence, "no correlated statement must yield no linked evidence")
}

func TestObservationEconomicRelayLinkedStatementLateArrivalIsDiscovered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newBridgeMeteringStore(t, "bridge-store")
	source := bridgeRuntimeObservation("late-source", 1)
	require.NoError(t, store.AppendObservation(ctx, source))

	relay := &observationEconomicRelay{journal: store}
	evidence := make([]metering.Observation, 0)
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.Empty(t, evidence, "statement evidence not yet imported")

	statement := bridgeStatementObservation(t, "line-late", "b", "bridge-store", 1)
	require.NoError(t, store.AppendObservation(ctx, statement))
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.True(t, bridgeEvidenceContains(evidence, statement.IdentityKey()), "late statement must be discovered on the next lookup")
}

func TestObservationEconomicRelayLinkedStatementCorrectionRevisionIsDiscovered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newBridgeMeteringStore(t, "bridge-store")
	source := bridgeRuntimeObservation("correction-source", 1)
	require.NoError(t, store.AppendObservation(ctx, source))

	statement := bridgeStatementObservation(t, "line-correct", "b", "bridge-store", 1)
	require.NoError(t, store.AppendObservation(ctx, statement))

	correction := bridgeStatementObservation(t, "line-correct", "b", "bridge-store", 2)
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{{
		StoreID: "bridge-store", ObservationID: statement.ID, Revision: statement.Revision, PayloadHash: statement.Fingerprint(),
	}}
	require.NoError(t, correction.Validate())
	require.NoError(t, store.AppendObservation(ctx, correction))

	relay := &observationEconomicRelay{journal: store}
	evidence := make([]metering.Observation, 0)
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.True(t, bridgeEvidenceContains(evidence, statement.IdentityKey()), "base statement revision must be linked")
	require.True(t, bridgeEvidenceContains(evidence, correction.IdentityKey()), "correction revision for the same B-leg must be linked")
}

func TestObservationEconomicRelayLinkedStatementResumesAcrossPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newBridgeMeteringStore(t, "bridge-store")
	source := bridgeRuntimeObservation("resume-source", 1)
	require.NoError(t, store.AppendObservation(ctx, source))

	const total = observationEconomicEvidenceLimit + 1
	batch := make([]metering.Observation, 0, total)
	for i := 1; i <= total; i++ {
		batch = append(batch, bridgeStatementObservation(t, fmt.Sprintf("line-resume-%03d", i), "b-resume", "bridge-store", uint64(i)))
	}
	require.NoError(t, store.AppendObservations(ctx, batch))

	relay := &observationEconomicRelay{journal: store}
	evidence := make([]metering.Observation, 0)
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b-resume", &evidence))
	require.Len(t, evidence, total, "all linked statement revisions must be discovered across a page boundary")
}

func TestObservationEconomicRelayLinkedStatementScopeIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newBridgeMeteringStore(t, "bridge-store")
	source := bridgeRuntimeObservation("isolation-source", 1)
	source.Correlation.ProviderRequestID = "request-source"
	require.NoError(t, source.Validate())
	require.NoError(t, store.AppendObservation(ctx, source))

	linked := bridgeStatementObservation(t, "line-linked", "b", "bridge-store", 1)
	require.NoError(t, store.AppendObservation(ctx, linked))

	// Same provider account but a different correlated B-leg must not leak in.
	otherBLeg := bridgeStatementObservation(t, "line-other-bleg", "other-bleg", "bridge-store", 1)
	require.NoError(t, store.AppendObservation(ctx, otherBLeg))

	// Same B-leg but a conflicting provider request must be rejected by lineage.
	conflicting := bridgeStatementObservation(t, "line-conflict", "b", "bridge-store", 1)
	conflicting.Correlation.ProviderRequestID = "request-other"
	require.NoError(t, conflicting.Validate())
	require.NoError(t, store.AppendObservation(ctx, conflicting))

	relay := &observationEconomicRelay{journal: store}
	evidence := make([]metering.Observation, 0)
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.True(t, bridgeEvidenceContains(evidence, linked.IdentityKey()))
	require.False(t, bridgeEvidenceContains(evidence, otherBLeg.IdentityKey()), "a different B-leg statement must not be linked")
	require.False(t, bridgeEvidenceContains(evidence, conflicting.IdentityKey()), "a conflicting provider request must not be linked")
}

// appendBridgeUnrelatedHistory appends count observations that share a provider
// account but carry distinct B-legs, modelling unrelated account history that
// the old provider-account scan had to page through.
func appendBridgeUnrelatedHistory(ctx context.Context, store *journalstore.DurableStore, providerAccount string, start, count int) error {
	const chunk = 250
	for offset := 0; offset < count; offset += chunk {
		size := min(count-offset, chunk)
		batch := make([]metering.Observation, 0, size)
		for i := 0; i < size; i++ {
			index := start + offset + i
			observation := bridgeRuntimeObservation(fmt.Sprintf("unrelated-%06d", index), uint64(index+1))
			observation.StreamID = "bridge-unrelated-stream"
			observation.Subject.BLegID = fmt.Sprintf("unrelated-bleg-%06d", index)
			observation.Correlation.BLegID = observation.Subject.BLegID
			observation.Subject.ProviderAccountKey = providerAccount
			observation.Correlation.ProviderAccountKey = providerAccount
			if err := observation.Validate(); err != nil {
				return err
			}
			batch = append(batch, observation)
		}
		if err := store.AppendObservations(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

func bridgeStatementObservation(t *testing.T, lineID, blegID, storeID string, revision uint64) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_002_000+int64(revision), 0).UTC()
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:statement", Unit: metering.UnitToken, SchemaID: "bridge:v1"}
	value := metering.Decimal{Coefficient: "7", Scale: 0}
	observation := metering.Observation{
		Version: 2, ID: fmt.Sprintf("statement-%s-%d", lineID, revision),
		SourceEventKey: fmt.Sprintf("statement-source-%s-%d", lineID, revision),
		Revision:       revision, StreamID: "bridge-statement-stream", Sequence: revision,
		Origin: metering.OriginStatement, Acquisition: metering.AcquisitionStatementImporter,
		Authority:   metering.AuthorityVerifiedStatement,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectStatementLine, StoreID: storeID, ProviderAccountKey: "provider",
			StatementID: "statement-1", StatementLineID: lineID,
		},
		Correlation: metering.CorrelationV2{StoreID: storeID, ProviderAccountKey: "provider", BLegID: blegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "bridge:v1",
		Measures: []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
	}
	require.NoError(t, observation.Validate())
	return observation
}

func bridgeEvidenceContains(evidence []metering.Observation, identityKey string) bool {
	for _, observation := range evidence {
		if observation.IdentityKey() == identityKey {
			return true
		}
	}
	return false
}
