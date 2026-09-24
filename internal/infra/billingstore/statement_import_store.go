package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Task 13.1B durable normalized statement import ledger. One immutable
// statement revision envelope plus its independent immutable line claims are
// retained atomically; an exact replay is a no-op and a changed payload under
// an existing identity fails closed with billing.ErrStatementImportConflict.

// ErrStatementImportMismatch identifies a retained statement import row that is
// not the canonical self-validating record it claims to be.
var ErrStatementImportMismatch = errors.New("billingstore: statement import record mismatch")

const (
	statementRevisionSchemaVersion = 1
	statementImportLineQueryChunk  = 256
	statementImportTxAttempts      = 40
	statementImportTxDelay         = 3 * time.Millisecond
)

var _ billing.StatementImportLedger = (*DurableStore)(nil)

// statementScopeWire is the durable authorized scope carried by a retained
// statement revision. It contains authorization material only; the statement
// payload itself remains in envelope_json.
type statementScopeWire struct {
	StoreID             string   `json:"store_id"`
	TenantID            string   `json:"tenant_id,omitempty"`
	PrincipalID         string   `json:"principal_id,omitempty"`
	ProviderAccountKeys []string `json:"provider_account_keys,omitempty"`
}

func statementScopeWireOf(scope billing.TrustedStatementScope) statementScopeWire {
	return statementScopeWire{
		StoreID:             scope.StoreID,
		TenantID:            scope.TenantID,
		PrincipalID:         scope.PrincipalID,
		ProviderAccountKeys: scope.AuthorizedProviderAccounts(),
	}
}

func (w statementScopeWire) toDomain() billing.TrustedStatementScope {
	return billing.TrustedStatementScope{
		StoreID:             w.StoreID,
		TenantID:            w.TenantID,
		PrincipalID:         w.PrincipalID,
		ProviderAccountKeys: append([]string(nil), w.ProviderAccountKeys...),
	}
}

type statementRevisionRow struct {
	ID                 int64  `bun:"id"`
	StoreID            string `bun:"store_id"`
	StatementKey       string `bun:"statement_key"`
	ProviderAccountKey string `bun:"provider_account_key"`
	StatementID        string `bun:"statement_id"`
	PeriodID           string `bun:"period_id"`
	Revision           int64  `bun:"revision"`
	TenantID           string `bun:"tenant_id"`
	PrincipalID        string `bun:"principal_id"`
	SchemaVersion      int64  `bun:"schema_version"`
	Fingerprint        string `bun:"fingerprint"`
	ScopeJSON          string `bun:"scope_json"`
	EnvelopeJSON       string `bun:"envelope_json"`
	ReceivedAt         int64  `bun:"received_at_unix"`
}

type statementLineFingerprintRow struct {
	LineKey     string `bun:"line_key"`
	Fingerprint string `bun:"fingerprint"`
}

const statementRevisionSelect = `SELECT id, store_id, statement_key, provider_account_key, statement_id, period_id, revision, tenant_id, principal_id, schema_version, fingerprint, scope_json, envelope_json, received_at_unix FROM billing_statement_revisions`

const insertStatementRevisionSQL = `INSERT INTO billing_statement_revisions(store_id, statement_key, provider_account_key, statement_id, period_id, revision, tenant_id, principal_id, schema_version, fingerprint, scope_json, envelope_json, received_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`

const insertStatementLineSQL = `INSERT INTO billing_statement_lines(store_id, line_key, statement_key, envelope_revision, provider_account_key, statement_id, period_id, tenant_id, line_id, line_revision, outcome, charge_item_id, observation_id, observation_revision, fingerprint, payload_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`

func validateStatementIdentityForStore(storeID string, identity economics.StatementIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if identity.StoreID != storeID {
		return fmt.Errorf("%w: statement identity store", ErrEconomicsOutOfScope)
	}
	return nil
}

func validateStatementLineIdentityForStore(storeID string, identity economics.StatementLineIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if identity.StoreID != storeID {
		return fmt.Errorf("%w: statement line identity store", ErrEconomicsOutOfScope)
	}
	return nil
}

// LookupStatementRevision returns the retained fingerprint for one immutable
// statement revision identity. A missing identity is not an error.
func (s *DurableStore) LookupStatementRevision(ctx context.Context, identity economics.StatementIdentity) (string, bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return "", false, err
	}
	if err := validateStatementIdentityForStore(s.storeID, identity); err != nil {
		return "", false, err
	}
	var fingerprint string
	err := s.db.NewRaw(`SELECT fingerprint FROM billing_statement_revisions WHERE store_id = ? AND statement_key = ? LIMIT 1`, s.storeID, identity.Key()).Scan(ctx, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("billingstore: statement revision lookup: %w", err)
	}
	return fingerprint, true, nil
}

// LookupStatementLines returns the retained fingerprint for each found line
// revision identity, keyed by StatementLineIdentity.Key. The query is an
// exact, index-backed key probe bounded to the supplied identity set.
func (s *DurableStore) LookupStatementLines(ctx context.Context, identities []economics.StatementLineIdentity) (map[string]string, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	retained := make(map[string]string, len(identities))
	if len(identities) == 0 {
		return retained, nil
	}
	keys := make([]string, len(identities))
	for i, identity := range identities {
		if err := validateStatementLineIdentityForStore(s.storeID, identity); err != nil {
			return nil, err
		}
		keys[i] = identity.Key()
	}
	for start := 0; start < len(keys); start += statementImportLineQueryChunk {
		end := min(start+statementImportLineQueryChunk, len(keys))
		chunk := keys[start:end]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, s.storeID)
		for _, key := range chunk {
			args = append(args, key)
		}
		query := `SELECT line_key, fingerprint FROM billing_statement_lines WHERE store_id = ? AND line_key IN (` + sqlPlaceholders(len(chunk)) + `)`
		var rows []statementLineFingerprintRow
		if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
			return nil, fmt.Errorf("billingstore: statement line lookup: %w", err)
		}
		for _, row := range rows {
			retained[row.LineKey] = row.Fingerprint
		}
	}
	return retained, nil
}

// AppendStatementRevision atomically retains one normalized statement revision
// and its line claims. Exact replay is an idempotent no-op; a retained
// statement or line identity with different content fails closed with
// billing.ErrStatementImportConflict and leaves no partial insert.
func (s *DurableStore) AppendStatementRevision(ctx context.Context, statement billing.NormalizedStatement) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	normalized := statement.Clone()
	if err := normalized.Validate(); err != nil {
		return err
	}
	if normalized.Identity.StoreID != s.storeID {
		return fmt.Errorf("%w: statement identity store", ErrEconomicsOutOfScope)
	}
	if normalized.Identity.Revision > math.MaxInt64 {
		return fmt.Errorf("billingstore: statement revision exceeds database range")
	}
	for _, line := range normalized.Lines {
		if line.Identity.Revision > math.MaxInt64 {
			return fmt.Errorf("billingstore: statement line revision exceeds database range")
		}
	}
	payloads, err := buildStatementImportPayloads(normalized)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := range statementImportTxAttempts {
		err := s.appendStatementRevisionOnce(ctx, normalized, payloads)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isStatementImportSQLiteContention(s, err) || attempt == statementImportTxAttempts-1 {
			return err
		}
		if err := waitContention(ctx, time.Duration(attempt+1)*statementImportTxDelay); err != nil {
			return err
		}
	}
	return lastErr
}

type statementImportPayloads struct {
	scopeJSON    string
	envelopeJSON string
	linePayloads []string
}

func buildStatementImportPayloads(statement billing.NormalizedStatement) (statementImportPayloads, error) {
	scopeJSON, err := json.Marshal(statementScopeWireOf(statement.Scope))
	if err != nil {
		return statementImportPayloads{}, fmt.Errorf("billingstore: statement scope JSON: %w", err)
	}
	envelopeJSON, err := json.Marshal(statement.Batch)
	if err != nil {
		return statementImportPayloads{}, fmt.Errorf("billingstore: statement envelope JSON: %w", err)
	}
	linePayloads := make([]string, len(statement.Lines))
	for i, line := range statement.Lines {
		payload, err := json.Marshal(line.Line)
		if err != nil {
			return statementImportPayloads{}, fmt.Errorf("billingstore: statement line payload JSON: %w", err)
		}
		linePayloads[i] = string(payload)
	}
	return statementImportPayloads{scopeJSON: string(scopeJSON), envelopeJSON: string(envelopeJSON), linePayloads: linePayloads}, nil
}

func (s *DurableStore) appendStatementRevisionOnce(ctx context.Context, statement billing.NormalizedStatement, payloads statementImportPayloads) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: statement revision begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.NewRaw(insertStatementRevisionSQL,
		s.storeID, statement.Identity.Key(), statement.Identity.ProviderAccountKey, statement.Identity.StatementID,
		statement.Identity.PeriodID, int64(statement.Identity.Revision), statement.Scope.TenantID, statement.Scope.PrincipalID,
		statementRevisionSchemaVersion, statement.Fingerprint, payloads.scopeJSON, payloads.envelopeJSON,
		time.Now().UTC().UnixNano()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert statement revision: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: statement revision rows affected: %w", err)
	}
	if inserted == 0 {
		// A concurrent or previously retained revision won; it must be the
		// exact same immutable record including all of its line claims.
		retained, err := loadStatementRevisionTx(ctx, tx, s.storeID, statement.Identity.Key())
		if err != nil {
			return err
		}
		if err := verifyStatementRevisionRow(retained, statement, payloads); err != nil {
			return err
		}
		return verifyRetainedStatementLinesTx(ctx, tx, s.storeID, statement)
	}

	for i, line := range statement.Lines {
		lineResult, err := tx.NewRaw(insertStatementLineSQL,
			s.storeID, line.Identity.Key(), statement.Identity.Key(), int64(statement.Identity.Revision),
			line.Identity.ProviderAccountKey, line.Identity.StatementID, line.Identity.PeriodID, statement.Scope.TenantID,
			line.Identity.LineID, int64(line.Identity.Revision), string(line.Line.Outcome), line.Line.ChargeItemID,
			line.Line.Observation.ObservationID, int64(line.Line.Observation.Revision), line.Fingerprint, payloads.linePayloads[i]).Exec(ctx)
		if err != nil {
			return fmt.Errorf("billingstore: insert statement line: %w", err)
		}
		if _, err := lineResult.RowsAffected(); err != nil {
			return fmt.Errorf("billingstore: statement line rows affected: %w", err)
		}
		var stored string
		if err := tx.NewRaw(`SELECT fingerprint FROM billing_statement_lines WHERE store_id = ? AND line_key = ? LIMIT 1`, s.storeID, line.Identity.Key()).Scan(ctx, &stored); err != nil {
			return fmt.Errorf("billingstore: statement line replay lookup: %w", err)
		}
		if stored != line.Fingerprint {
			return &billing.StatementImportConflictError{StatementKey: statement.Identity.Key(), LineIDs: []string{line.Line.ID}}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: statement revision commit: %w", err)
	}
	return nil
}

func loadStatementRevisionTx(ctx context.Context, tx bun.Tx, storeID, statementKey string) (statementRevisionRow, error) {
	var row statementRevisionRow
	err := tx.NewRaw(statementRevisionSelect+` WHERE store_id = ? AND statement_key = ? LIMIT 1`, storeID, statementKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return statementRevisionRow{}, fmt.Errorf("%w: retained statement revision disappeared", ErrStatementImportMismatch)
	}
	if err != nil {
		return statementRevisionRow{}, fmt.Errorf("billingstore: retained statement revision lookup: %w", err)
	}
	return row, nil
}

// verifyStatementRevisionRow binds a retained envelope row to the incoming
// canonical record. A different fingerprint is a caller conflict; any other
// identity, scope or generation drift is a durable-record mismatch.
func verifyStatementRevisionRow(row statementRevisionRow, statement billing.NormalizedStatement, payloads statementImportPayloads) error {
	if row.Fingerprint != statement.Fingerprint {
		return &billing.StatementImportConflictError{StatementKey: statement.Identity.Key()}
	}
	for name, mismatch := range map[string]bool{
		"schema version":     row.SchemaVersion != statementRevisionSchemaVersion,
		"store":              row.StoreID != statement.Identity.StoreID,
		"statement key":      row.StatementKey != statement.Identity.Key(),
		"provider account":   row.ProviderAccountKey != statement.Identity.ProviderAccountKey,
		"statement id":       row.StatementID != statement.Identity.StatementID,
		"period":             row.PeriodID != statement.Identity.PeriodID,
		"revision":           row.Revision != int64(statement.Identity.Revision),
		"tenant":             row.TenantID != statement.Scope.TenantID,
		"principal":          row.PrincipalID != statement.Scope.PrincipalID,
		"scope":              row.ScopeJSON != payloads.scopeJSON,
		"canonical envelope": row.EnvelopeJSON != payloads.envelopeJSON,
	} {
		if mismatch {
			return fmt.Errorf("%w: retained statement %s does not match the canonical record", ErrStatementImportMismatch, name)
		}
	}
	return nil
}

func verifyRetainedStatementLinesTx(ctx context.Context, tx bun.Tx, storeID string, statement billing.NormalizedStatement) error {
	for _, line := range statement.Lines {
		var stored string
		err := tx.NewRaw(`SELECT fingerprint FROM billing_statement_lines WHERE store_id = ? AND line_key = ? LIMIT 1`, storeID, line.Identity.Key()).Scan(ctx, &stored)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: retained statement line %q is missing", ErrStatementImportMismatch, line.Line.ID)
		}
		if err != nil {
			return fmt.Errorf("billingstore: retained statement line lookup: %w", err)
		}
		if stored != line.Fingerprint {
			return fmt.Errorf("%w: retained statement line %q fingerprint", ErrStatementImportMismatch, line.Line.ID)
		}
	}
	return nil
}

// GetStatementRevision reads one retained statement revision and re-derives it
// through the domain normalizer before returning it. A row that is not the
// canonical self-validating record fails closed with
// ErrStatementImportMismatch instead of being returned as evidence.
func (s *DurableStore) GetStatementRevision(ctx context.Context, identity economics.StatementIdentity) (billing.NormalizedStatement, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.NormalizedStatement{}, err
	}
	if err := validateStatementIdentityForStore(s.storeID, identity); err != nil {
		return billing.NormalizedStatement{}, err
	}
	var row statementRevisionRow
	err := s.db.NewRaw(statementRevisionSelect+` WHERE store_id = ? AND statement_key = ? LIMIT 1`, s.storeID, identity.Key()).Scan(ctx, &row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.NormalizedStatement{}, sql.ErrNoRows
		}
		return billing.NormalizedStatement{}, fmt.Errorf("billingstore: statement revision read: %w", err)
	}
	return s.statementRevisionFromRow(ctx, identity, row)
}

func (s *DurableStore) statementRevisionFromRow(ctx context.Context, identity economics.StatementIdentity, row statementRevisionRow) (billing.NormalizedStatement, error) {
	if row.SchemaVersion != statementRevisionSchemaVersion {
		return billing.NormalizedStatement{}, fmt.Errorf("%w: unsupported statement revision schema %d", ErrStatementImportMismatch, row.SchemaVersion)
	}
	var scopeWire statementScopeWire
	if err := json.Unmarshal([]byte(row.ScopeJSON), &scopeWire); err != nil {
		return billing.NormalizedStatement{}, fmt.Errorf("%w: statement scope decode", ErrStatementImportMismatch)
	}
	var batch economics.StatementBatch
	if err := json.Unmarshal([]byte(row.EnvelopeJSON), &batch); err != nil {
		return billing.NormalizedStatement{}, fmt.Errorf("%w: statement envelope decode", ErrStatementImportMismatch)
	}
	normalized, err := billing.NormalizeStatement(scopeWire.toDomain(), batch)
	if err != nil {
		return billing.NormalizedStatement{}, fmt.Errorf("%w: statement evidence: %v", ErrStatementImportMismatch, err)
	}
	if normalized.Identity != identity {
		return billing.NormalizedStatement{}, fmt.Errorf("%w: requested identity does not match the retained record", ErrStatementImportMismatch)
	}
	payloads, err := buildStatementImportPayloads(normalized)
	if err != nil {
		return billing.NormalizedStatement{}, fmt.Errorf("%w: %v", ErrStatementImportMismatch, err)
	}
	if err := verifyStatementRevisionRow(row, normalized, payloads); err != nil {
		return billing.NormalizedStatement{}, err
	}
	retained, err := s.LookupStatementLines(ctx, statementLineIdentities(normalized))
	if err != nil {
		return billing.NormalizedStatement{}, err
	}
	if len(retained) != len(normalized.Lines) {
		return billing.NormalizedStatement{}, fmt.Errorf("%w: retained statement line count does not match the envelope", ErrStatementImportMismatch)
	}
	for _, line := range normalized.Lines {
		if retained[line.Identity.Key()] != line.Fingerprint {
			return billing.NormalizedStatement{}, fmt.Errorf("%w: retained statement line %q fingerprint", ErrStatementImportMismatch, line.Line.ID)
		}
	}
	return normalized, nil
}

func statementLineIdentities(statement billing.NormalizedStatement) []economics.StatementLineIdentity {
	identities := make([]economics.StatementLineIdentity, len(statement.Lines))
	for i, line := range statement.Lines {
		identities[i] = line.Identity
	}
	return identities
}

func isStatementImportSQLiteContention(store *DurableStore, err error) bool {
	if err == nil || store == nil || store.db == nil {
		return false
	}
	if store.db.Dialect().Name() != dialect.SQLite {
		return false
	}
	return isSQLiteBusy(err) || strings.Contains(strings.ToLower(err.Error()), "deadlock")
}
