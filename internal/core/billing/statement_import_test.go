package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type statementImportFixtureOptions struct {
	Store         string
	Tenant        string
	Account       string
	Statement     string
	Period        string
	Revision      uint64
	Amount        string
	ObservationID string
	LineRevision  uint64
	Received      time.Time
}

func statementImportFixture(t *testing.T, opts statementImportFixtureOptions) economics.StatementBatch {
	t.Helper()

	if opts.Store == "" {
		opts.Store = "store-1"
	}
	if opts.Tenant == "" {
		opts.Tenant = "tenant-1"
	}
	if opts.Account == "" {
		opts.Account = "provider-account"
	}
	if opts.Statement == "" {
		opts.Statement = "statement-1"
	}
	if opts.Period == "" {
		opts.Period = "period-1"
	}
	if opts.Revision == 0 {
		opts.Revision = 1
	}
	if opts.Amount == "" {
		opts.Amount = "1.25"
	}
	if opts.ObservationID == "" {
		opts.ObservationID = "statement-observation-1"
	}
	if opts.LineRevision == 0 {
		opts.LineRevision = 1
	}
	if opts.Received.IsZero() {
		opts.Received = time.Unix(1_700_000_000, 0).UTC()
	}

	observation := statementImportObservation(t, opts)
	ref := metering.ObservationRef{
		StoreID:       observation.Subject.StoreID,
		ObservationID: observation.ID,
		Revision:      observation.Revision,
		PayloadHash:   observation.Fingerprint(),
	}
	subject := observation.Subject
	unmatchedSubject := subject
	unmatchedSubject.StatementLineID = "line-2"

	return economics.StatementBatch{
		Version:            1,
		ProviderAccountKey: opts.Account,
		StatementID:        opts.Statement,
		Revision:           opts.Revision,
		PeriodID:           opts.Period,
		Subject:            subject,
		Observations:       []metering.Observation{observation},
		Lines: []economics.StatementLine{
			{
				ID: "line-1", Revision: opts.LineRevision, Subject: subject,
				Observation: ref, ChargeItemID: "charge-1", Outcome: economics.StatementLineMatched,
			},
			{
				ID: "line-2", Revision: 1, Subject: unmatchedSubject,
				Outcome: economics.StatementLineUnmatched, UnmatchedReason: "account-period aggregate",
			},
		},
	}
}

func statementImportObservation(t *testing.T, opts statementImportFixtureOptions) metering.Observation {
	t.Helper()

	component := metering.ComponentKey{
		Direction: metering.DirectionOutput,
		Component: metering.ComponentAudio,
		Unit:      metering.UnitSecond,
		SchemaID:  "statement:audio:v1",
	}
	amount, err := metering.ParseDecimal(opts.Amount)
	require.NoError(t, err)

	return metering.Observation{
		Version:        2,
		ID:             opts.ObservationID,
		SourceEventKey: "statement-event-" + opts.ObservationID,
		Revision:       1,
		StreamID:       "statement-stream-1",
		Sequence:       1,
		Origin:         metering.OriginStatement,
		Acquisition:    metering.AcquisitionStatementImporter,
		Authority:      metering.AuthorityVerifiedStatement,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendIngress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectStatementLine, StoreID: opts.Store, TenantID: opts.Tenant,
			ProviderAccountKey: opts.Account, StatementID: opts.Statement,
			StatementLineID: "line-1", PeriodID: opts.Period,
		},
		Correlation: metering.CorrelationV2{
			StoreID: opts.Store, TenantID: opts.Tenant,
			ProviderAccountKey: opts.Account, PeriodID: opts.Period,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
		ReceivedAt: opts.Received,
		MappingRef: "statement:test:v1",
		Measures: []metering.Measure{{
			Key: component, Value: decimalPointer(t, "1"), Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "charge-1", Amount: &amount, Currency: "USD",
			Kind: metering.ChargeKindComponent, Component: &component,
		}},
	}
}

func decimalPointer(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	require.NoError(t, err)
	return &value
}

func trustedStatementImportScope() TrustedStatementScope {
	return TrustedStatementScope{
		StoreID:             "store-1",
		TenantID:            "tenant-1",
		PrincipalID:         "principal-1",
		ProviderAccountKeys: []string{"provider-account"},
	}
}

type memoryStatementLedger struct {
	statements map[string]string
	lines      map[string]string
	appends    int
	appendErr  error
	lookupErr  error
}

func newMemoryStatementLedger() *memoryStatementLedger {
	return &memoryStatementLedger{statements: map[string]string{}, lines: map[string]string{}}
}

func (m *memoryStatementLedger) LookupStatementRevision(ctx context.Context, identity economics.StatementIdentity) (string, bool, error) {
	if m.lookupErr != nil {
		return "", false, m.lookupErr
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	fingerprint, found := m.statements[identity.Key()]
	return fingerprint, found, nil
}

func (m *memoryStatementLedger) LookupStatementLines(ctx context.Context, identities []economics.StatementLineIdentity) (map[string]string, error) {
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	retained := make(map[string]string, len(identities))
	for _, identity := range identities {
		if fingerprint, found := m.lines[identity.Key()]; found {
			retained[identity.Key()] = fingerprint
		}
	}
	return retained, nil
}

func (m *memoryStatementLedger) AppendStatementRevision(ctx context.Context, statement NormalizedStatement) error {
	if m.appendErr != nil {
		return m.appendErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := statement.Validate(); err != nil {
		return err
	}
	statementKey := statement.Identity.Key()
	if fingerprint, found := m.statements[statementKey]; found {
		if fingerprint != statement.Fingerprint {
			return &StatementImportConflictError{StatementKey: statementKey}
		}
		return nil
	}
	for _, line := range statement.Lines {
		if fingerprint, found := m.lines[line.Identity.Key()]; found && fingerprint != line.Fingerprint {
			return &StatementImportConflictError{StatementKey: statementKey, LineIDs: []string{line.Line.ID}}
		}
	}
	m.statements[statementKey] = statement.Fingerprint
	for _, line := range statement.Lines {
		key := line.Identity.Key()
		if _, found := m.lines[key]; !found {
			m.lines[key] = line.Fingerprint
		}
	}
	m.appends++
	return nil
}

func newStatementImportService(t *testing.T, ledger StatementImportLedger) *StatementImportService {
	t.Helper()
	service, err := NewStatementImportService(ledger)
	require.NoError(t, err)
	require.NotNil(t, service)
	return service
}

func TestStatementImportAcceptsIndependentNormalizedFixture(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	scope := trustedStatementImportScope()
	batch := statementImportFixture(t, statementImportFixtureOptions{})

	result, err := service.Import(context.Background(), scope, batch)
	require.NoError(t, err)
	require.NoError(t, result.Validate())
	assert.Equal(t, []string{"line-1"}, result.Accepted)
	assert.Equal(t, []string{"line-2"}, result.Unmatched)
	assert.Empty(t, result.Replayed)
	assert.Empty(t, result.Rejected)
	assert.Equal(t, 1, ledger.appends)

	normalized, err := NormalizeStatement(scope, batch)
	require.NoError(t, err)
	require.NoError(t, normalized.Validate())
	require.Len(t, normalized.Lines, 2)

	// The normalized record stays independent from request/call evidence: no
	// B-leg, attempt, request or call allocation is invented anywhere.
	for i, observation := range normalized.Batch.Observations {
		assert.Equal(t, metering.SubjectStatementLine, observation.Subject.Kind)
		assert.Empty(t, observation.Subject.BLegID, "observation %d B-leg", i)
		assert.Empty(t, observation.Subject.BillingCallID, "observation %d billing call", i)
		assert.Empty(t, observation.Subject.RequestID, "observation %d request", i)
		assert.Empty(t, observation.Subject.ALegID, "observation %d A-leg", i)
		assert.Empty(t, observation.Correlation.BLegID, "observation %d correlation B-leg", i)
		assert.Empty(t, observation.Correlation.BillingCallID, "observation %d correlation billing call", i)
	}
	for i, line := range normalized.Lines {
		require.Greater(t, len(normalized.Batch.Lines), i)
		assert.Equal(t, normalized.Batch.Lines[i].ID, line.Line.ID, "normalized line %d is not carried by the canonical batch", i)
		assert.Equal(t, normalized.Batch.Lines[i].Revision, line.Line.Revision)
	}
	assert.Contains(t, ledger.statements, normalized.Identity.Key())
	assert.Equal(t, normalized.Fingerprint, ledger.statements[normalized.Identity.Key()])
	assert.Len(t, ledger.lines, 2)
}

func TestStatementImportExactReplayIsIdempotent(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	scope := trustedStatementImportScope()

	first, err := service.Import(context.Background(), scope, statementImportFixture(t, statementImportFixtureOptions{}))
	require.NoError(t, err)
	require.Equal(t, []string{"line-1"}, first.Accepted)

	// Transport receipt metadata is not economic identity: a later receipt of
	// the identical claim content is a replay, not a conflict.
	replayed := statementImportFixture(t, statementImportFixtureOptions{
		Received: time.Unix(1_700_000_000, 0).UTC().Add(3 * time.Hour),
	})
	result, err := service.Import(context.Background(), scope, replayed)
	require.NoError(t, err)
	assert.Equal(t, []string{"line-1", "line-2"}, result.Replayed)
	assert.Empty(t, result.Accepted)
	assert.Empty(t, result.Unmatched)
	assert.Empty(t, result.Rejected)
	assert.Equal(t, 1, ledger.appends, "an exact replay must not write again")
	assert.Len(t, ledger.statements, 1)
	assert.Len(t, ledger.lines, 2)
}

func TestStatementImportConflictingStatementRevisionFailsClosed(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	scope := trustedStatementImportScope()

	_, err := service.Import(context.Background(), scope, statementImportFixture(t, statementImportFixtureOptions{Amount: "1.25"}))
	require.NoError(t, err)
	identity, err := statementImportFixture(t, statementImportFixtureOptions{}).Identity()
	require.NoError(t, err)
	originalFingerprint := ledger.statements[identity.Key()]

	conflicting := statementImportFixture(t, statementImportFixtureOptions{Amount: "2.50"})
	result, err := service.Import(context.Background(), scope, conflicting)
	require.ErrorIs(t, err, ErrStatementImportConflict)
	assert.Equal(t, []string{"line-1", "line-2"}, result.Rejected, "a statement-level conflict rejects the whole envelope")
	var conflict *StatementImportConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, identity.Key(), conflict.StatementKey)

	assert.Equal(t, 1, ledger.appends)
	assert.Equal(t, originalFingerprint, ledger.statements[identity.Key()], "the retained revision must stay immutable")
}

func TestStatementImportLineConflictAcrossStatementRevisionsFailsClosed(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	scope := trustedStatementImportScope()

	_, err := service.Import(context.Background(), scope, statementImportFixture(t, statementImportFixtureOptions{}))
	require.NoError(t, err)

	// A later statement revision restates the same line revision with changed
	// economic content. The line identity is immutable, so the restatement
	// conflicts even though the statement envelope identity is new.
	restated := statementImportFixture(t, statementImportFixtureOptions{
		Revision: 2, ObservationID: "statement-observation-2", Amount: "2.50",
	})
	_, err = service.Import(context.Background(), scope, restated)
	require.ErrorIs(t, err, ErrStatementImportConflict)
	assert.Equal(t, 1, ledger.appends)
	assert.Len(t, ledger.statements, 1, "the conflicting statement revision must not be retained")

	// A genuine new claim uses a new line revision and is accepted; the
	// unchanged unmatched line replays against its retained line revision.
	corrected := statementImportFixture(t, statementImportFixtureOptions{
		Revision: 2, ObservationID: "statement-observation-2", Amount: "2.50", LineRevision: 2,
	})
	result, err := service.Import(context.Background(), scope, corrected)
	require.NoError(t, err)
	assert.Equal(t, []string{"line-1"}, result.Accepted)
	assert.Equal(t, []string{"line-2"}, result.Replayed)
	assert.Equal(t, 2, ledger.appends)
	assert.Len(t, ledger.statements, 2)
	assert.Len(t, ledger.lines, 3)
}

func TestStatementImportScopeFailsClosed(t *testing.T) {
	t.Parallel()

	baseBatch := statementImportFixture(t, statementImportFixtureOptions{})

	tests := []struct {
		name    string
		scope   TrustedStatementScope
		mutate  func(*economics.StatementBatch)
		wantErr error
	}{
		{
			name:    "foreign store in trusted scope",
			scope:   TrustedStatementScope{StoreID: "store-2", TenantID: "tenant-1"},
			wantErr: ErrStatementImportScopeMismatch,
		},
		{
			name:    "unauthorized provider account",
			scope:   TrustedStatementScope{StoreID: "store-1", TenantID: "tenant-1", ProviderAccountKeys: []string{"other-account"}},
			wantErr: ErrStatementImportScopeMismatch,
		},
		{
			name:    "trusted tenant mismatch",
			scope:   TrustedStatementScope{StoreID: "store-1", TenantID: "tenant-2"},
			wantErr: ErrStatementImportScopeMismatch,
		},
		{
			name:  "tenant scoped import without tenant claim",
			scope: TrustedStatementScope{StoreID: "store-1", TenantID: "tenant-1"},
			mutate: func(b *economics.StatementBatch) {
				clearStatementImportTenant(b)
			},
			wantErr: ErrStatementImportScopeMismatch,
		},
		{
			name:  "inconsistent tenant claims",
			scope: TrustedStatementScope{StoreID: "store-1", TenantID: "tenant-1"},
			mutate: func(b *economics.StatementBatch) {
				b.Subject.TenantID = "tenant-2"
			},
			wantErr: ErrStatementImportScopeMismatch,
		},
		{
			name:    "scope without tenant or account authority",
			scope:   TrustedStatementScope{StoreID: "store-1"},
			wantErr: ErrStatementImportInvalid,
		},
		{
			name:  "self-inconsistent batch store",
			scope: trustedStatementImportScope(),
			mutate: func(b *economics.StatementBatch) {
				b.Observations[0].Subject.StoreID = "store-2"
			},
			wantErr: ErrStatementImportInvalid,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ledger := newMemoryStatementLedger()
			service := newStatementImportService(t, ledger)
			batch := baseBatch
			batch.Observations = slices.Clone(baseBatch.Observations)
			batch.Lines = slices.Clone(baseBatch.Lines)
			if tc.mutate != nil {
				tc.mutate(&batch)
			}
			_, err := service.Import(context.Background(), tc.scope, batch)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Empty(t, ledger.statements)
			assert.Empty(t, ledger.lines)
			assert.Zero(t, ledger.appends)
		})
	}
}

func clearStatementImportTenant(batch *economics.StatementBatch) {
	batch.Subject.TenantID = ""
	for i := range batch.Observations {
		batch.Observations[i].Subject.TenantID = ""
		batch.Observations[i].Correlation.TenantID = ""
	}
	for i := range batch.Lines {
		batch.Lines[i].Subject.TenantID = ""
	}
	for i := range batch.Lines {
		if batch.Lines[i].Outcome == economics.StatementLineUnmatched {
			continue
		}
		for _, observation := range batch.Observations {
			if observation.ID == batch.Lines[i].Observation.ObservationID && observation.Revision == batch.Lines[i].Observation.Revision {
				batch.Lines[i].Observation.PayloadHash = observation.Fingerprint()
				break
			}
		}
	}
}

func TestStatementImportRejectsMalformedAndUnsupportedClaims(t *testing.T) {
	t.Parallel()

	baseBatch := statementImportFixture(t, statementImportFixtureOptions{})

	unsupportedGranularity := statementImportFixture(t, statementImportFixtureOptions{})
	unsupportedGranularity.Observations = append(slices.Clone(unsupportedGranularity.Observations), metering.Observation{
		Version:        2,
		ID:             "b-leg-observation",
		SourceEventKey: "b-leg-event",
		Revision:       1,
		StreamID:       "stream-1",
		Sequence:       1,
		Origin:         metering.OriginStatement,
		Acquisition:    metering.AcquisitionStatementImporter,
		Authority:      metering.AuthorityVerifiedStatement,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendIngress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "store-1", TenantID: "tenant-1",
			ProviderAccountKey: "provider-account", BLegID: "b-1", PeriodID: "period-1",
		},
		Correlation: metering.CorrelationV2{
			StoreID: "store-1", TenantID: "tenant-1", ProviderAccountKey: "provider-account",
			BLegID: "b-1", PeriodID: "period-1",
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
		ReceivedAt: time.Unix(1_700_000_000, 0).UTC(),
		MappingRef: "statement:test:v1",
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction: metering.DirectionOutput, Component: metering.ComponentAudio,
				Unit: metering.UnitSecond, SchemaID: "statement:audio:v1",
			},
			Value: decimalPointer(t, "1"), Quality: metering.QualityObserved,
		}},
	})

	overbound := statementImportFixture(t, statementImportFixtureOptions{})
	overbound.Lines = make([]economics.StatementLine, economics.MaxStatementLines+1)
	for i := range overbound.Lines {
		overbound.Lines[i] = economics.StatementLine{
			ID: fmt.Sprintf("line-%d", i), Revision: 1,
			Subject: func() metering.SubjectRef {
				subject := overbound.Subject
				subject.StatementLineID = fmt.Sprintf("line-%d", i)
				return subject
			}(),
			Outcome: economics.StatementLineUnmatched, UnmatchedReason: "aggregate",
		}
	}

	invalidAmount := statementImportFixture(t, statementImportFixtureOptions{})
	invalidAmount.Observations[0].Charges = slices.Clone(invalidAmount.Observations[0].Charges)
	invalidAmount.Observations[0].Charges[0].Amount = &metering.Decimal{Coefficient: "1.2.5", Scale: 1}
	invalidAmount.Observations[0].Measures = nil

	providerOrigin := statementImportFixture(t, statementImportFixtureOptions{})
	providerOrigin.Observations[0].Origin = metering.OriginProvider
	providerOrigin.Observations[0].Acquisition = metering.AcquisitionProviderResponse
	providerOrigin.Observations[0].Authority = metering.AuthorityObservedClaim

	missingCharge := statementImportFixture(t, statementImportFixtureOptions{})
	missingCharge.Lines[0].ChargeItemID = "charge-missing"

	periodMismatch := statementImportFixture(t, statementImportFixtureOptions{})
	periodMismatch.Lines[0].Subject.PeriodID = "period-2"

	accountMismatch := statementImportFixture(t, statementImportFixtureOptions{})
	accountMismatch.Lines[0].Subject.ProviderAccountKey = "other-account"

	observationPeriodMismatch := statementImportFixture(t, statementImportFixtureOptions{})
	observationPeriodMismatch.Observations[0].Correlation.PeriodID = "period-2"

	conflictingObservation := statementImportFixture(t, statementImportFixtureOptions{})
	conflictingObservation.Observations = append(slices.Clone(conflictingObservation.Observations), func() metering.Observation {
		conflict := conflictingObservation.Observations[0]
		conflict.SourceEventKey = "statement-event-conflict"
		return conflict
	}())

	duplicateLine := statementImportFixture(t, statementImportFixtureOptions{})
	duplicateLine.Lines[1].ID = "line-1"

	zeroLineRevision := statementImportFixture(t, statementImportFixtureOptions{LineRevision: 1})
	zeroLineRevision.Lines[1].Revision = 0

	emptyBatch := statementImportFixture(t, statementImportFixtureOptions{})
	emptyBatch.Observations = nil
	emptyBatch.Lines = nil

	tests := []struct {
		name  string
		batch economics.StatementBatch
	}{
		{name: "zero version", batch: func() economics.StatementBatch {
			batch := baseBatch
			batch.Version = 0
			return batch
		}()},
		{name: "zero statement revision", batch: func() economics.StatementBatch {
			batch := baseBatch
			batch.Revision = 0
			return batch
		}()},
		{name: "empty statement", batch: emptyBatch},
		{name: "duplicate line id", batch: duplicateLine},
		{name: "zero line revision", batch: zeroLineRevision},
		{name: "linked charge missing from observation", batch: missingCharge},
		{name: "line period mismatch", batch: periodMismatch},
		{name: "line provider account mismatch", batch: accountMismatch},
		{name: "conflicting observation revision", batch: conflictingObservation},
		{name: "observation period mismatch", batch: observationPeriodMismatch},
		{name: "invalid exact decimal", batch: invalidAmount},
		{name: "provider origin relabeled as statement", batch: providerOrigin},
		{name: "unsupported request-scoped granularity", batch: unsupportedGranularity},
		{name: "statement line bound exceeded", batch: overbound},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ledger := newMemoryStatementLedger()
			service := newStatementImportService(t, ledger)
			_, err := service.Import(context.Background(), trustedStatementImportScope(), tc.batch)
			require.ErrorIs(t, err, ErrStatementImportInvalid)
			assert.Empty(t, ledger.statements)
			assert.Zero(t, ledger.appends)
		})
	}
}

func TestStatementImportNormalizedRecordSelfValidates(t *testing.T) {
	t.Parallel()

	scope := trustedStatementImportScope()
	normalized, err := NormalizeStatement(scope, statementImportFixture(t, statementImportFixtureOptions{}))
	require.NoError(t, err)
	require.NoError(t, normalized.Validate())

	tamperedFingerprint := normalized
	tamperedFingerprint.Fingerprint = strings.Repeat("0", 64)
	require.ErrorIs(t, tamperedFingerprint.Validate(), ErrStatementImportInvalid)

	tamperedIdentity := normalized
	tamperedIdentity.Identity.Revision = 9
	require.ErrorIs(t, tamperedIdentity.Validate(), ErrStatementImportInvalid)

	tamperedLine := normalized
	tamperedLine.Lines = slices.Clone(normalized.Lines)
	tamperedLine.Lines[0].Fingerprint = strings.Repeat("1", 64)
	require.ErrorIs(t, tamperedLine.Validate(), ErrStatementImportInvalid)

	tamperedBatch := normalized
	tamperedBatch.Batch = normalized.Batch
	tamperedBatch.Batch.Lines = slices.Clone(normalized.Batch.Lines)
	tamperedBatch.Batch.Lines[0].ChargeItemID = "charge-tampered"
	require.ErrorIs(t, tamperedBatch.Validate(), ErrStatementImportInvalid)
}

func TestStatementImportNormalizedRecordCloneIsDetached(t *testing.T) {
	t.Parallel()

	normalized, err := NormalizeStatement(trustedStatementImportScope(), statementImportFixture(t, statementImportFixtureOptions{}))
	require.NoError(t, err)

	clone := normalized.Clone()
	clone.Scope.ProviderAccountKeys[0] = "other-account"
	clone.Batch.Observations[0].Charges[0].ChargeItemID = "charge-mutated"
	clone.Batch.Lines[0].ID = "line-mutated"
	clone.Lines[0].Fingerprint = strings.Repeat("f", 64)

	assert.Equal(t, "provider-account", normalized.Scope.ProviderAccountKeys[0])
	assert.Equal(t, "charge-1", normalized.Batch.Observations[0].Charges[0].ChargeItemID)
	assert.Equal(t, "line-1", normalized.Batch.Lines[0].ID)
	require.NoError(t, normalized.Validate())
}

func TestStatementImportDeterministicAcrossInputOrder(t *testing.T) {
	t.Parallel()

	scope := trustedStatementImportScope()
	batch := statementImportFixture(t, statementImportFixtureOptions{})

	reordered := batch
	reordered.Observations = slices.Clone(batch.Observations)
	reordered.Lines = slices.Clone(batch.Lines)
	slices.Reverse(reordered.Observations)
	slices.Reverse(reordered.Lines)

	first, err := NormalizeStatement(scope, batch)
	require.NoError(t, err)
	second, err := NormalizeStatement(scope, reordered)
	require.NoError(t, err)
	assert.Equal(t, first.Identity, second.Identity)
	assert.Equal(t, first.Fingerprint, second.Fingerprint)
	assert.Equal(t, first.Batch.Observations, second.Batch.Observations)
	assert.Equal(t, first.Batch.Lines, second.Batch.Lines)

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	_, err = service.Import(context.Background(), scope, batch)
	require.NoError(t, err)
	replay, err := service.Import(context.Background(), scope, reordered)
	require.NoError(t, err)
	assert.Equal(t, []string{"line-1", "line-2"}, replay.Replayed)
	assert.Empty(t, replay.Accepted)
	assert.Equal(t, 1, ledger.appends)
}

func TestStatementImportRejectsInvalidConstructionAndContext(t *testing.T) {
	t.Parallel()

	if _, err := NewStatementImportService(nil); err == nil {
		t.Fatal("nil ledger must be rejected")
	}
	if _, err := NewStatementImportService((*memoryStatementLedger)(nil)); err == nil {
		t.Fatal("typed-nil ledger must be rejected")
	}

	service := newStatementImportService(t, newMemoryStatementLedger())
	_, err := service.Import(nil, trustedStatementImportScope(), statementImportFixture(t, statementImportFixtureOptions{})) //nolint:staticcheck // deliberately exercises nil-context rejection
	require.ErrorIs(t, err, ErrStatementImportInvalid)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.Import(canceled, trustedStatementImportScope(), statementImportFixture(t, statementImportFixtureOptions{}))
	require.ErrorIs(t, err, context.Canceled)
}

func TestStatementImportSurfacesLedgerFailure(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	ledger.appendErr = errors.New("durable append unavailable")
	service := newStatementImportService(t, ledger)
	result, err := service.Import(context.Background(), trustedStatementImportScope(), statementImportFixture(t, statementImportFixtureOptions{}))
	require.ErrorIs(t, err, ErrStatementImportLedgerUnavailable)
	assert.ErrorContains(t, err, "durable append unavailable")
	assert.Empty(t, result.Accepted)
	assert.Empty(t, ledger.statements)

	lookupFailure := newMemoryStatementLedger()
	lookupFailure.lookupErr = errors.New("durable lookup unavailable")
	service = newStatementImportService(t, lookupFailure)
	_, err = service.Import(context.Background(), trustedStatementImportScope(), statementImportFixture(t, statementImportFixtureOptions{}))
	require.ErrorIs(t, err, ErrStatementImportLedgerUnavailable)
}

// boundStatementImporter is the test-only scope binding that satisfies the
// frozen public economics.StatementImporter seam. Production composition
// supplies the same binding when the protected import route is added.
type boundStatementImporter struct {
	service *StatementImportService
	scope   TrustedStatementScope
}

func (b *boundStatementImporter) Import(ctx context.Context, in economics.StatementBatch) (economics.ImportResult, error) {
	return b.service.Import(ctx, b.scope, in)
}

var _ economics.StatementImporter = (*boundStatementImporter)(nil)

func TestStatementImportSatisfiesPublicStatementImporterSeam(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	var importer economics.StatementImporter = &boundStatementImporter{service: service, scope: trustedStatementImportScope()}

	result, err := importer.Import(context.Background(), statementImportFixture(t, statementImportFixtureOptions{}))
	require.NoError(t, err)
	assert.Equal(t, []string{"line-1"}, result.Accepted)
	assert.Equal(t, []string{"line-2"}, result.Unmatched)
}

func TestStatementImportScopeHelpers(t *testing.T) {
	t.Parallel()

	scope := TrustedStatementScope{
		StoreID: "store-1", TenantID: "tenant-1", PrincipalID: "principal-1",
		ProviderAccountKeys: []string{"account-b", "account-a"},
	}
	require.NoError(t, scope.Validate())
	assert.True(t, scope.AuthorizesProviderAccount("account-a"))
	assert.False(t, scope.AuthorizesProviderAccount("account-c"))

	// The scope owns its authorization input; callers cannot widen it through
	// the returned slice.
	accounts := scope.AuthorizedProviderAccounts()
	slices.Sort(accounts)
	assert.Equal(t, []string{"account-a", "account-b"}, accounts)
	accounts[0] = "mutated"
	assert.True(t, scope.AuthorizesProviderAccount("account-a"))

	for name, invalid := range map[string]TrustedStatementScope{
		"empty":             {},
		"padded store":      {StoreID: " store-1", TenantID: "tenant-1"},
		"duplicate account": {StoreID: "store-1", ProviderAccountKeys: []string{"a", "a"}},
		"unsafe account":    {StoreID: "store-1", ProviderAccountKeys: []string{"bad\naccount"}},
	} {
		name, invalid := name, invalid
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, invalid.Validate(), ErrStatementImportInvalid)
		})
	}
}
