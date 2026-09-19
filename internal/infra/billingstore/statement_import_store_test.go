package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

var _ billing.StatementImportLedger = (*DurableStore)(nil)

type statementImportLineSpec struct {
	ID              string
	Revision        uint64
	Amount          string
	Unmatched       bool
	UnmatchedReason string
}

func statementImportDefaultLines() []statementImportLineSpec {
	return []statementImportLineSpec{
		{ID: "line-1", Revision: 1, Amount: "1.25"},
		{ID: "line-2", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	}
}

func statementImportNormalized(t *testing.T, storeID, statement string, revision uint64, lines ...statementImportLineSpec) billing.NormalizedStatement {
	t.Helper()
	if len(lines) == 0 {
		lines = statementImportDefaultLines()
	}
	scope := billing.TrustedStatementScope{
		StoreID: storeID, TenantID: "tenant-1", PrincipalID: "principal-1",
		ProviderAccountKeys: []string{"provider-account"},
	}
	batch := economics.StatementBatch{
		Version: 1, ProviderAccountKey: "provider-account", StatementID: statement,
		Revision: revision, PeriodID: "period-1",
	}
	for i, spec := range lines {
		subject := metering.SubjectRef{
			Kind: metering.SubjectStatementLine, StoreID: storeID, TenantID: "tenant-1",
			ProviderAccountKey: "provider-account", StatementID: statement,
			StatementLineID: spec.ID, PeriodID: "period-1",
		}
		if i == 0 {
			batch.Subject = subject
		}
		lineRevision := spec.Revision
		if lineRevision == 0 {
			lineRevision = 1
		}
		if spec.Unmatched {
			batch.Lines = append(batch.Lines, economics.StatementLine{
				ID: spec.ID, Revision: lineRevision, Subject: subject,
				Outcome: economics.StatementLineUnmatched, UnmatchedReason: spec.UnmatchedReason,
			})
			continue
		}
		observation := statementImportObservation(t, storeID, statement, spec.ID, spec.Amount)
		ref := metering.ObservationRef{
			StoreID: storeID, ObservationID: observation.ID, Revision: observation.Revision,
			PayloadHash: observation.Fingerprint(),
		}
		batch.Observations = append(batch.Observations, observation)
		batch.Lines = append(batch.Lines, economics.StatementLine{
			ID: spec.ID, Revision: lineRevision, Subject: subject,
			Observation: ref, ChargeItemID: "charge-" + spec.ID, Outcome: economics.StatementLineMatched,
		})
	}
	normalized, err := billing.NormalizeStatement(scope, batch)
	require.NoError(t, err)
	return normalized
}

func statementImportObservation(t *testing.T, storeID, statement, lineID, amount string) metering.Observation {
	t.Helper()
	value, err := metering.ParseDecimal(amount)
	require.NoError(t, err)
	component := metering.ComponentKey{
		Direction: metering.DirectionOutput, Component: metering.ComponentAudio,
		Unit: metering.UnitSecond, SchemaID: "statement:audio:v1",
	}
	received := time.Unix(1_700_000_000, 0).UTC()
	return metering.Observation{
		Version: 2, ID: "obs-" + lineID, SourceEventKey: "event-" + statement + "-" + lineID,
		Revision: 1, StreamID: "statement-stream-1", Sequence: 1,
		Origin:      metering.OriginStatement,
		Acquisition: metering.AcquisitionStatementImporter,
		Authority:   metering.AuthorityVerifiedStatement,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectStatementLine, StoreID: storeID, TenantID: "tenant-1",
			ProviderAccountKey: "provider-account", StatementID: statement,
			StatementLineID: lineID, PeriodID: "period-1",
		},
		Correlation: metering.CorrelationV2{
			StoreID: storeID, TenantID: "tenant-1",
			ProviderAccountKey: "provider-account", PeriodID: "period-1",
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: received, ReceivedAt: received, MappingRef: "statement:test:v1",
		Measures: []metering.Measure{{Key: component, Value: &value, Quality: metering.QualityObserved}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "charge-" + lineID, Amount: &value, Currency: "USD",
			Kind: metering.ChargeKindComponent, Component: &component,
		}},
	}
}

func statementImportRevisionCount(t *testing.T, store *DurableStore, statementID string) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.NewRaw(
		`SELECT COUNT(1) FROM billing_statement_revisions WHERE store_id = ? AND statement_id = ?`,
		store.StoreID(), statementID).Scan(context.Background(), &count))
	return count
}

func statementImportLineRows(t *testing.T, store *DurableStore) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.NewRaw(
		`SELECT COUNT(1) FROM billing_statement_lines WHERE store_id = ?`, store.StoreID()).Scan(context.Background(), &count))
	return count
}

func TestStatementImportLedgerSQLiteReplayConflictAndRestart(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, "test", "statement-1", 1)

	require.NoError(t, store.AppendStatementRevision(ctx, normalized))
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	fingerprint, found, err := store.LookupStatementRevision(ctx, normalized.Identity)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, normalized.Fingerprint, fingerprint)

	identities := []economics.StatementLineIdentity{normalized.Lines[0].Identity, normalized.Lines[1].Identity}
	retained, err := store.LookupStatementLines(ctx, identities)
	require.NoError(t, err)
	require.Len(t, retained, 2)
	require.Equal(t, normalized.Lines[0].Fingerprint, retained[identities[0].Key()])
	require.Equal(t, normalized.Lines[1].Fingerprint, retained[identities[1].Key()])

	require.NoError(t, store.AppendStatementRevision(ctx, normalized), "exact replay must be an idempotent no-op")
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	// Same statement revision identity with changed content conflicts and
	// leaves the retained revision untouched.
	statementConflict := statementImportNormalized(t, "test", "statement-1", 1,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"},
		statementImportLineSpec{ID: "line-2", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	)
	err = store.AppendStatementRevision(ctx, statementConflict)
	require.ErrorIs(t, err, billing.ErrStatementImportConflict)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))
	unchanged, found, err := store.LookupStatementRevision(ctx, normalized.Identity)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, normalized.Fingerprint, unchanged)

	// A new statement revision that restates the same line revision with
	// changed content conflicts and must not leave a partial statement row.
	revisionConflict := statementImportNormalized(t, "test", "statement-1", 2,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"},
		statementImportLineSpec{ID: "line-2", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	)
	err = store.AppendStatementRevision(ctx, revisionConflict)
	require.ErrorIs(t, err, billing.ErrStatementImportConflict)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	// A genuine new line revision is accepted, and the unchanged unmatched
	// line replays against its retained line revision.
	accepted := statementImportNormalized(t, "test", "statement-1", 2,
		statementImportLineSpec{ID: "line-1", Revision: 2, Amount: "2.50"},
		statementImportLineSpec{ID: "line-2", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	)
	require.NoError(t, store.AppendStatementRevision(ctx, accepted))
	require.Equal(t, 2, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 3, statementImportLineRows(t, store))

	// Restart over the same database handle: durable rows and replay identity
	// survive without in-memory state.
	reopened, err := openStore(ctx, store.db, Config{StoreID: "test"})
	require.NoError(t, err)
	got, err := reopened.GetStatementRevision(ctx, normalized.Identity)
	require.NoError(t, err)
	require.Equal(t, normalized.Identity, got.Identity)
	require.Equal(t, normalized.Fingerprint, got.Fingerprint)
	require.Len(t, got.Lines, 2)
	require.NoError(t, reopened.AppendStatementRevision(ctx, normalized), "restart replay must stay an idempotent no-op")
	require.Equal(t, 2, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 3, statementImportLineRows(t, store))
	require.NoError(t, VerifySchema(ctx, store.db))
}

func TestStatementImportLedgerSQLitePartialBatchRollsBack(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.AppendStatementRevision(ctx, statementImportNormalized(t, "test", "statement-1", 1)))
	require.Equal(t, 2, statementImportLineRows(t, store))

	// One conflicting line revision plus one genuinely new line must leave no
	// statement row and no new line row.
	mixed := statementImportNormalized(t, "test", "statement-1", 2,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"},
		statementImportLineSpec{ID: "line-3", Revision: 1, Amount: "5"},
	)
	err := store.AppendStatementRevision(ctx, mixed)
	require.ErrorIs(t, err, billing.ErrStatementImportConflict)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store), "no partial line insert")

	// The same holds when an existing line replays and another conflicts.
	replayAndConflict := statementImportNormalized(t, "test", "statement-1", 3,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"},
		statementImportLineSpec{ID: "line-2", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	)
	err = store.AppendStatementRevision(ctx, replayAndConflict)
	require.ErrorIs(t, err, billing.ErrStatementImportConflict)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))
}

func TestStatementImportLedgerSQLiteStoreAndScopeIsolation(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()

	foreign := statementImportNormalized(t, "other-store", "statement-1", 1)
	require.ErrorIs(t, store.AppendStatementRevision(ctx, foreign), ErrEconomicsOutOfScope)
	_, _, err := store.LookupStatementRevision(ctx, foreign.Identity)
	require.ErrorIs(t, err, ErrEconomicsOutOfScope)
	_, err = store.GetStatementRevision(ctx, foreign.Identity)
	require.ErrorIs(t, err, ErrEconomicsOutOfScope)
	require.Zero(t, statementImportRevisionCount(t, store, "statement-1"))

	missing := statementImportNormalized(t, "test", "statement-missing", 1)
	fingerprint, found, err := store.LookupStatementRevision(ctx, missing.Identity)
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, fingerprint)
	_, err = store.GetStatementRevision(ctx, missing.Identity)
	require.ErrorIs(t, err, sql.ErrNoRows)

	foreignLines, err := store.LookupStatementLines(ctx, []economics.StatementLineIdentity{foreign.Lines[0].Identity})
	require.ErrorIs(t, err, ErrEconomicsOutOfScope)
	require.Nil(t, foreignLines)

	if _, _, err := store.LookupStatementRevision(ctx, economics.StatementIdentity{}); err == nil {
		t.Fatal("invalid identity must be rejected before SQL")
	}
}

func TestStatementImportLedgerSQLiteRetainsIndependentEvidenceAndOutcome(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, "test", "statement-1", 1)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))

	got, err := store.GetStatementRevision(ctx, normalized.Identity)
	require.NoError(t, err)
	require.NoError(t, got.Validate())
	require.Equal(t, normalized.Fingerprint, got.Fingerprint)
	require.Equal(t, normalized.Scope.StoreID, got.Scope.StoreID)
	require.Equal(t, normalized.Scope.TenantID, got.Scope.TenantID)
	require.Equal(t, normalized.Scope.PrincipalID, got.Scope.PrincipalID)

	outcomes := map[string]economics.StatementLineOutcome{}
	for _, line := range got.Lines {
		outcomes[line.Line.ID] = line.Line.Outcome
	}
	require.Equal(t, economics.StatementLineMatched, outcomes["line-1"])
	require.Equal(t, economics.StatementLineUnmatched, outcomes["line-2"])

	for i, observation := range got.Batch.Observations {
		require.Equal(t, metering.SubjectStatementLine, observation.Subject.Kind, "observation %d", i)
		require.Empty(t, observation.Subject.BLegID)
		require.Empty(t, observation.Subject.BillingCallID)
		require.Empty(t, observation.Subject.RequestID)
		require.Empty(t, observation.Correlation.BLegID)
	}

	// The durable envelope is the canonical batch bytes; a raw rewrite of the
	// retained payload is rejected by the self-validating read.
	forgedIdentity := economics.StatementIdentity{
		StoreID: "test", ProviderAccountKey: "provider-account", StatementID: "statement-forged",
		PeriodID: "period-1", Revision: 1,
	}
	insertRawStatementRevision(t, store, "test", forgedIdentity.Key(), "statement-forged",
		forgeStatementEnvelopeJSON(t, normalized), normalized.Fingerprint, "tenant-1", "principal-1")
	_, err = store.GetStatementRevision(ctx, forgedIdentity)
	require.ErrorIs(t, err, ErrStatementImportMismatch)
}

func TestStatementImportLedgerSQLiteReadRejectsIncompleteOrDriftedRows(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, "test", "statement-drift", 1)

	// Raw rows are written but one line is missing and the other carries a
	// drifted fingerprint. Neither the read nor a replay may treat that record
	// as valid statement evidence.
	envelope, err := json.Marshal(normalized.Batch)
	require.NoError(t, err)
	insertRawStatementRevision(t, store, "test", normalized.Identity.Key(), "statement-drift",
		envelope, normalized.Fingerprint, "tenant-1", "principal-1")
	insertRawStatementLine(t, store, "test", normalized.Identity.Key(), normalized, 0, strings.Repeat("0", 64))

	_, err = store.GetStatementRevision(ctx, normalized.Identity)
	require.ErrorIs(t, err, ErrStatementImportMismatch)

	require.ErrorIs(t, store.AppendStatementRevision(ctx, normalized), ErrStatementImportMismatch)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-drift"), "append must not add another revision row")
	require.Equal(t, 1, statementImportLineRows(t, store), "append must not add another line row")
}

func TestStatementImportLedgerSQLiteConcurrentIdenticalImportsHaveOneEffect(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, "test", "statement-1", 1)

	const workers = 4
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errs[index] = store.AppendStatementRevision(ctx, normalized)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "worker %d", i)
	}
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 2, statementImportLineRows(t, store))
	_, found, err := store.LookupStatementRevision(ctx, normalized.Identity)
	require.NoError(t, err)
	require.True(t, found)
}

func TestStatementImportLedgerSQLiteConcurrentConflictsHaveOneWinner(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	left := statementImportNormalized(t, "test", "statement-1", 1,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "1.25"})
	right := statementImportNormalized(t, "test", "statement-1", 1,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"})

	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, record := range []billing.NormalizedStatement{left, right} {
		wg.Add(1)
		go func(index int, statement billing.NormalizedStatement) {
			defer wg.Done()
			<-start
			errs[index] = store.AppendStatementRevision(ctx, statement)
		}(i, record)
	}
	close(start)
	wg.Wait()

	successes := 0
	conflicts := 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, billing.ErrStatementImportConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent append error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-1"))
	require.Equal(t, 1, statementImportLineRows(t, store))

	fingerprint, found, err := store.LookupStatementRevision(ctx, left.Identity)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, fingerprint == left.Fingerprint || fingerprint == right.Fingerprint,
		"retained revision must match exactly one winner")

	// Two different statement revisions of the same statement racing over one
	// line identity must also leave exactly one durable revision and no mixed
	// rows.
	second := newSQLiteTestStore(t)
	firstRevision := statementImportNormalized(t, "test", "statement-race", 1,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "1.25"})
	secondRevision := statementImportNormalized(t, "test", "statement-race", 2,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"})
	errs = make([]error, 2)
	start = make(chan struct{})
	for i, record := range []billing.NormalizedStatement{firstRevision, secondRevision} {
		wg.Add(1)
		go func(index int, statement billing.NormalizedStatement) {
			defer wg.Done()
			<-start
			errs[index] = second.AppendStatementRevision(ctx, statement)
		}(i, record)
	}
	close(start)
	wg.Wait()
	successes = 0
	conflicts = 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, billing.ErrStatementImportConflict):
			conflicts++
		default:
			t.Fatalf("unexpected cross-revision append error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	var revisions int
	require.NoError(t, second.db.NewRaw(`SELECT COUNT(1) FROM billing_statement_revisions`).Scan(ctx, &revisions))
	require.Equal(t, 1, revisions)
	require.Equal(t, 1, statementImportLineRows(t, second))
}

func TestStatementImportLedgerSQLiteSchemaAndImmutability(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.Contains(t, RequiredMigrationNames, BillingStatementImportMigrationName)
	require.NoError(t, VerifySchema(ctx, store.db))

	for _, table := range []string{"billing_statement_revisions", "billing_statement_lines"} {
		var name string
		require.NoError(t, store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &name))
		require.Equal(t, table, name)
	}
	for _, index := range []string{
		billingStatementRevisionScopeIndex, billingStatementRevisionTenantIndex,
		billingStatementLineStatementIndex, billingStatementLineScopeIndex,
	} {
		var name string
		require.NoError(t, store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(ctx, &name))
		require.Equal(t, index, name)
	}
	for _, trigger := range []string{
		"billing_statement_revisions_immutable_update", "billing_statement_revisions_immutable_delete",
		"billing_statement_lines_immutable_update", "billing_statement_lines_immutable_delete",
	} {
		var name string
		require.NoError(t, store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(ctx, &name))
		require.Equal(t, trigger, name)
	}

	require.NoError(t, store.AppendStatementRevision(ctx, statementImportNormalized(t, "test", "statement-1", 1)))
	_, err := store.db.ExecContext(ctx, `UPDATE billing_statement_revisions SET fingerprint = 'forged' WHERE store_id = ?`, store.StoreID())
	require.Error(t, err, "retained statement revisions must be immutable")
	_, err = store.db.ExecContext(ctx, `DELETE FROM billing_statement_lines WHERE store_id = ?`, store.StoreID())
	require.Error(t, err, "retained statement lines must be immutable")
}

func TestStatementImportLedgerSQLiteRejectsTamperedRecordsBeforeWrite(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, "test", "statement-1", 1)

	tamperedFingerprint := normalized
	tamperedFingerprint.Fingerprint = strings.Repeat("0", 64)
	require.ErrorIs(t, store.AppendStatementRevision(ctx, tamperedFingerprint), billing.ErrStatementImportInvalid)

	tamperedScope := normalized
	tamperedScope.Scope = billing.TrustedStatementScope{StoreID: "other-store", TenantID: "tenant-1"}
	require.ErrorIs(t, store.AppendStatementRevision(ctx, tamperedScope), billing.ErrStatementImportScopeMismatch)

	require.Zero(t, statementImportRevisionCount(t, store, "statement-1"))
	require.Zero(t, statementImportLineRows(t, store))
}

func TestStatementImportLedgerSQLitePublicImporterReplayIsStableAcrossRestart(t *testing.T) {
	t.Parallel()

	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, "test", "statement-public", 1)

	service, err := billing.NewStatementImportService(store)
	require.NoError(t, err)
	importer, err := billing.NewBoundStatementImporter(service, normalized.Scope)
	require.NoError(t, err)

	first, err := importer.Import(ctx, normalized.Batch)
	require.NoError(t, err)
	require.Equal(t, []string{"line-1"}, first.Accepted)
	require.Equal(t, []string{"line-2"}, first.Unmatched)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-public"))

	replayed, err := importer.Import(ctx, normalized.Batch)
	require.NoError(t, err)
	require.Equal(t, []string{"line-1", "line-2"}, replayed.Replayed)
	require.Empty(t, replayed.Accepted)
	require.Empty(t, replayed.Unmatched)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-public"))
	require.Equal(t, 2, statementImportLineRows(t, store))

	// Restart: a new service and adapter over the same durable ledger return
	// the same replay classification without rewriting anything.
	reopened, err := openStore(ctx, store.db, Config{StoreID: "test"})
	require.NoError(t, err)
	restartedService, err := billing.NewStatementImportService(reopened)
	require.NoError(t, err)
	restartedImporter, err := billing.NewBoundStatementImporter(restartedService, normalized.Scope)
	require.NoError(t, err)
	afterRestart, err := restartedImporter.Import(ctx, normalized.Batch)
	require.NoError(t, err)
	require.Equal(t, replayed, afterRestart)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-public"))

	// Changed content under the retained statement revision returns a typed
	// conflict through the public seam and writes nothing.
	conflicting := statementImportNormalized(t, "test", "statement-public", 1,
		statementImportLineSpec{ID: "line-1", Revision: 1, Amount: "2.50"},
		statementImportLineSpec{ID: "line-2", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	)
	result, err := restartedImporter.Import(ctx, conflicting.Batch)
	require.ErrorIs(t, err, billing.ErrStatementImportConflict)
	require.Equal(t, []string{"line-1", "line-2"}, result.Rejected)
	require.Equal(t, 1, statementImportRevisionCount(t, store, "statement-public"))
	require.Equal(t, 2, statementImportLineRows(t, store))
}

func forgeStatementEnvelopeJSON(t *testing.T, normalized billing.NormalizedStatement) []byte {
	t.Helper()
	var document map[string]any
	raw, err := json.Marshal(normalized.Batch)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &document))
	document["revision"] = float64(7)
	forged, err := json.Marshal(document)
	require.NoError(t, err)
	return forged
}

func insertRawStatementRevision(t *testing.T, store *DurableStore, storeID, statementKey, statementID string, envelope []byte, fingerprint, tenantID, principalID string) {
	t.Helper()
	scopeJSON, err := json.Marshal(struct {
		StoreID             string   `json:"store_id"`
		TenantID            string   `json:"tenant_id,omitempty"`
		PrincipalID         string   `json:"principal_id,omitempty"`
		ProviderAccountKeys []string `json:"provider_account_keys,omitempty"`
	}{StoreID: storeID, TenantID: tenantID, PrincipalID: principalID, ProviderAccountKeys: []string{"provider-account"}})
	require.NoError(t, err)
	_, err = store.db.ExecContext(context.Background(),
		`INSERT INTO billing_statement_revisions(store_id, statement_key, provider_account_key, statement_id, period_id, revision, tenant_id, principal_id, schema_version, fingerprint, scope_json, envelope_json, received_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		storeID, statementKey, "provider-account", statementID, "period-1", 1, tenantID, principalID, 1,
		fingerprint, string(scopeJSON), string(envelope), time.Unix(1_700_000_000, 0).UnixNano())
	require.NoError(t, err)
}

func insertRawStatementLine(t *testing.T, store *DurableStore, storeID, statementKey string, normalized billing.NormalizedStatement, lineIndex int, fingerprint string) {
	t.Helper()
	line := normalized.Lines[lineIndex]
	payload, err := json.Marshal(line.Line)
	require.NoError(t, err)
	_, err = store.db.ExecContext(context.Background(),
		`INSERT INTO billing_statement_lines(store_id, line_key, statement_key, envelope_revision, provider_account_key, statement_id, period_id, tenant_id, line_id, line_revision, outcome, charge_item_id, observation_id, observation_revision, fingerprint, payload_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		storeID, line.Identity.Key(), statementKey, normalized.Identity.Revision,
		line.Identity.ProviderAccountKey, line.Identity.StatementID, line.Identity.PeriodID,
		"tenant-1", line.Identity.LineID, int64(line.Identity.Revision), string(line.Line.Outcome),
		line.Line.ChargeItemID, line.Line.Observation.ObservationID, int64(line.Line.Observation.Revision),
		fingerprint, string(payload))
	require.NoError(t, err)
}
