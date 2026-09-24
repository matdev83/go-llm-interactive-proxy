package billing

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.1A normalized statement ingestion domain. It accepts already
// normalized statement evidence through the public economics.StatementBatch
// contract, validates an explicit trusted scope, computes deterministic
// statement/line replay identities and reports replay/conflict outcomes. It
// contains no SQL, vendor parsing, matching, posting or worker behavior; a
// persistence adapter implements StatementImportLedger behind this port.

var (
	// ErrStatementImportInvalid identifies a malformed, overbound or
	// unsupported normalized statement claim.
	ErrStatementImportInvalid = errors.New("billing: invalid statement import")
	// ErrStatementImportScopeMismatch identifies evidence outside the trusted
	// caller scope. Imports fail closed rather than trusting statement-claimed
	// tenant/account identity.
	ErrStatementImportScopeMismatch = errors.New("billing: statement import scope mismatch")
	// ErrStatementImportConflict identifies a retained immutable identity whose
	// content differs from the submitted revision. No durable change is made.
	ErrStatementImportConflict = errors.New("billing: statement import revision conflict")
	// ErrStatementImportLedgerUnavailable identifies a missing or failing
	// durable statement ledger.
	ErrStatementImportLedgerUnavailable = errors.New("billing: statement import ledger unavailable")
)

// TrustedStatementScope is the explicit authorization context of one import.
// Every field is copied from authenticated caller state, never from the
// statement payload; a statement cannot choose or widen its own scope. A scope
// must carry tenant or provider-account authority; a store-only scope is
// rejected so an import cannot silently authorize every account.
type TrustedStatementScope struct {
	StoreID             string
	TenantID            string
	PrincipalID         string
	ProviderAccountKeys []string
}

// Validate checks the trusted scope before any statement content is trusted.
func (s TrustedStatementScope) Validate() error {
	if !validTrustedStatementScopeIdentity(s.StoreID) {
		return fmt.Errorf("%w: trusted store scope required", ErrStatementImportInvalid)
	}
	if s.TenantID != "" && !validTrustedStatementScopeIdentity(s.TenantID) {
		return fmt.Errorf("%w: trusted tenant scope is not a bounded identity", ErrStatementImportInvalid)
	}
	if s.PrincipalID != "" && !validTrustedStatementScopeIdentity(s.PrincipalID) {
		return fmt.Errorf("%w: trusted principal is not a bounded identity", ErrStatementImportInvalid)
	}
	seen := make(map[string]struct{}, len(s.ProviderAccountKeys))
	for _, account := range s.ProviderAccountKeys {
		if !validTrustedStatementScopeIdentity(account) {
			return fmt.Errorf("%w: trusted provider account is not a bounded identity", ErrStatementImportInvalid)
		}
		if _, exists := seen[account]; exists {
			return fmt.Errorf("%w: duplicate trusted provider account", ErrStatementImportInvalid)
		}
		seen[account] = struct{}{}
	}
	if s.TenantID == "" && len(s.ProviderAccountKeys) == 0 {
		return fmt.Errorf("%w: trusted tenant or provider account scope required", ErrStatementImportInvalid)
	}
	return nil
}

// AuthorizesProviderAccount reports whether the trusted scope covers one
// provider account. An empty account list means tenant-scoped authority.
func (s TrustedStatementScope) AuthorizesProviderAccount(account string) bool {
	if len(s.ProviderAccountKeys) == 0 {
		return true
	}
	return slices.Contains(s.ProviderAccountKeys, account)
}

// AuthorizedProviderAccounts returns a detached copy of the authorized
// account list so callers cannot widen the scope after construction.
func (s TrustedStatementScope) AuthorizedProviderAccounts() []string {
	return slices.Clone(s.ProviderAccountKeys)
}

// Clone returns a detached scope value.
func (s TrustedStatementScope) Clone() TrustedStatementScope {
	out := s
	out.ProviderAccountKeys = slices.Clone(s.ProviderAccountKeys)
	return out
}

func validTrustedStatementScopeIdentity(value string) bool {
	return validEconomicIdentity(value, metering.MaxSchemaIDBytes) && strings.TrimSpace(value) == value
}

// NormalizedStatementLine is one canonical statement line with its immutable
// line identity and deterministic replay fingerprint.
type NormalizedStatementLine struct {
	Identity    economics.StatementLineIdentity
	Fingerprint string
	Line        economics.StatementLine
}

// NormalizedStatement is the canonical, scope-checked statement revision ready
// for atomic durable retention. Observations are statement-line evidence only:
// no request, A-leg or B-leg allocation is invented here, and unmatched lines
// remain explicit rather than being attached to a guessed charge.
type NormalizedStatement struct {
	Identity    economics.StatementIdentity
	Fingerprint string
	Scope       TrustedStatementScope
	Batch       economics.StatementBatch
	Lines       []NormalizedStatementLine
}

// Validate re-derives identity and fingerprints from the canonical batch so
// tampered retained records are rejected before they can drive matching or
// posting.
func (n NormalizedStatement) Validate() error {
	if err := n.Scope.Validate(); err != nil {
		return err
	}
	canonical, err := n.Batch.Canonical()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
	}
	if err := validateStatementImportShape(canonical); err != nil {
		return err
	}
	if err := validateStatementImportScope(n.Scope, canonical); err != nil {
		return err
	}
	identity, err := canonical.Identity()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
	}
	if identity != n.Identity {
		return fmt.Errorf("%w: statement identity does not match the canonical batch", ErrStatementImportInvalid)
	}
	fingerprints, err := canonical.ReplayFingerprints()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
	}
	if fingerprints.Statement != n.Fingerprint {
		return fmt.Errorf("%w: statement fingerprint does not match the canonical batch", ErrStatementImportInvalid)
	}
	if len(n.Lines) != len(canonical.Lines) {
		return fmt.Errorf("%w: normalized line count does not match the canonical batch", ErrStatementImportInvalid)
	}
	for i, line := range n.Lines {
		lineIdentity, err := line.Line.Identity(identity)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
		}
		if !lineIdentity.Equal(line.Identity) {
			return fmt.Errorf("%w: normalized line identity does not match the canonical batch", ErrStatementImportInvalid)
		}
		if fingerprints.Lines[lineIdentity.Key()] != line.Fingerprint {
			return fmt.Errorf("%w: normalized line fingerprint does not match the canonical batch", ErrStatementImportInvalid)
		}
		if canonical.Lines[i].ID != line.Line.ID || canonical.Lines[i].Revision != line.Line.Revision {
			return fmt.Errorf("%w: normalized line order does not match the canonical batch", ErrStatementImportInvalid)
		}
	}
	return nil
}

// Clone returns a deep copy safe for durable adapters that hand the record to
// another owner.
func (n NormalizedStatement) Clone() NormalizedStatement {
	out := n
	out.Scope = n.Scope.Clone()
	out.Batch = n.Batch
	observations := make([]metering.Observation, len(n.Batch.Observations))
	for i, observation := range n.Batch.Observations {
		observations[i] = observation.Clone()
	}
	out.Batch.Observations = observations
	out.Batch.Lines = append([]economics.StatementLine(nil), n.Batch.Lines...)
	out.Lines = append([]NormalizedStatementLine(nil), n.Lines...)
	return out
}

// NormalizeStatement validates an authenticated normalized statement, applies
// the trusted scope, canonicalizes its evidence and derives deterministic
// statement/line replay identities. It performs no persistence.
func NormalizeStatement(scope TrustedStatementScope, batch economics.StatementBatch) (NormalizedStatement, error) {
	if err := scope.Validate(); err != nil {
		return NormalizedStatement{}, err
	}
	canonical, err := batch.Canonical()
	if err != nil {
		return NormalizedStatement{}, fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
	}
	if err := validateStatementImportShape(canonical); err != nil {
		return NormalizedStatement{}, err
	}
	if err := validateStatementImportScope(scope, canonical); err != nil {
		return NormalizedStatement{}, err
	}
	identity, err := canonical.Identity()
	if err != nil {
		return NormalizedStatement{}, fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
	}
	fingerprints, err := canonical.ReplayFingerprints()
	if err != nil {
		return NormalizedStatement{}, fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
	}
	lines := make([]NormalizedStatementLine, len(canonical.Lines))
	for i, line := range canonical.Lines {
		lineIdentity, err := line.Identity(identity)
		if err != nil {
			return NormalizedStatement{}, fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
		}
		fingerprint, ok := fingerprints.Lines[lineIdentity.Key()]
		if !ok {
			return NormalizedStatement{}, fmt.Errorf("%w: line fingerprint is missing from the canonical statement", ErrStatementImportInvalid)
		}
		lines[i] = NormalizedStatementLine{Identity: lineIdentity, Fingerprint: fingerprint, Line: line}
	}
	out := NormalizedStatement{
		Identity:    identity,
		Fingerprint: fingerprints.Statement,
		Scope:       scope.Clone(),
		Batch:       canonical,
		Lines:       lines,
	}
	if err := out.Validate(); err != nil {
		return NormalizedStatement{}, err
	}
	return out, nil
}

func validateStatementImportShape(batch economics.StatementBatch) error {
	if len(batch.Lines) == 0 && len(batch.Observations) == 0 {
		return fmt.Errorf("%w: statement carries no claim", ErrStatementImportInvalid)
	}
	for i, observation := range batch.Observations {
		if observation.Subject.Kind != metering.SubjectStatementLine {
			return fmt.Errorf("%w: observation %d uses unsupported statement granularity %q", ErrStatementImportInvalid, i, observation.Subject.Kind)
		}
		if observation.Origin != metering.OriginStatement ||
			observation.Acquisition != metering.AcquisitionStatementImporter ||
			observation.Authority != metering.AuthorityVerifiedStatement {
			return fmt.Errorf("%w: observation %d lacks authenticated statement provenance", ErrStatementImportInvalid, i)
		}
	}
	return nil
}

func validateStatementImportScope(scope TrustedStatementScope, batch economics.StatementBatch) error {
	if batch.Subject.StoreID != scope.StoreID {
		return fmt.Errorf("%w: statement store is outside the trusted scope", ErrStatementImportScopeMismatch)
	}
	if !scope.AuthorizesProviderAccount(batch.ProviderAccountKey) {
		return fmt.Errorf("%w: provider account is outside the trusted scope", ErrStatementImportScopeMismatch)
	}
	tenants := make(map[string]struct{}, 2)
	batchTenants := make(map[string]struct{}, 2)
	record := func(tenant string) {
		if tenant != "" {
			tenants[tenant] = struct{}{}
		}
	}
	recordBatch := func(tenant string) {
		if tenant != "" {
			tenants[tenant] = struct{}{}
			batchTenants[tenant] = struct{}{}
		}
	}
	recordBatch(batch.Subject.TenantID)
	for i, observation := range batch.Observations {
		if observation.Subject.StoreID != scope.StoreID || observation.Correlation.StoreID != scope.StoreID {
			return fmt.Errorf("%w: observation %d store is outside the trusted scope", ErrStatementImportScopeMismatch, i)
		}
		recordBatch(observation.Subject.TenantID)
		recordBatch(observation.Correlation.TenantID)
	}
	for i, line := range batch.Lines {
		if line.Subject.StoreID != scope.StoreID {
			return fmt.Errorf("%w: statement line %d store is outside the trusted scope", ErrStatementImportScopeMismatch, i)
		}
		if !scope.AuthorizesProviderAccount(line.Subject.ProviderAccountKey) {
			return fmt.Errorf("%w: statement line %d provider account is outside the trusted scope", ErrStatementImportScopeMismatch, i)
		}
		if scope.TenantID != "" && line.Subject.TenantID != scope.TenantID {
			return fmt.Errorf("%w: statement line %d tenant is outside the trusted scope", ErrStatementImportScopeMismatch, i)
		}
		record(line.Subject.TenantID)
	}
	if len(tenants) > 1 {
		return fmt.Errorf("%w: statement evidence mixes tenant scopes", ErrStatementImportScopeMismatch)
	}
	if scope.TenantID != "" {
		if _, ok := batchTenants[scope.TenantID]; !ok {
			if len(tenants) == 0 {
				return fmt.Errorf("%w: tenant-scoped import requires a tenant claim", ErrStatementImportScopeMismatch)
			}
			return fmt.Errorf("%w: trusted tenant does not match the statement", ErrStatementImportScopeMismatch)
		}
		if _, ok := tenants[scope.TenantID]; !ok {
			return fmt.Errorf("%w: trusted tenant does not match the statement", ErrStatementImportScopeMismatch)
		}
	}
	return nil
}

// StatementImportLedger is the consumer-owned durable port for normalized
// statement ingestion. Lookups are exact: an identity is either absent or
// retained with one fingerprint. AppendStatementRevision must atomically retain
// the statement revision and its line revisions, treat an exact replay as a
// no-op, and fail closed with ErrStatementImportConflict when a retained
// identity has different content; a partial write must be impossible.
type StatementImportLedger interface {
	LookupStatementRevision(context.Context, economics.StatementIdentity) (string, bool, error)
	LookupStatementLines(context.Context, []economics.StatementLineIdentity) (map[string]string, error)
	AppendStatementRevision(context.Context, NormalizedStatement) error
}

// StatementImporter is the host-side consumer-owned port for one
// authenticated statement import. Unlike the frozen external-module
// economics.StatementImporter seam, it requires the trusted scope explicitly
// at every call so a single service can serve many authenticated callers.
type StatementImporter interface {
	Import(context.Context, TrustedStatementScope, economics.StatementBatch) (economics.ImportResult, error)
}

// StatementImportService applies statement import policy over a durable
// ledger. It contains no persistence, matching, journal or worker behavior.
type StatementImportService struct {
	ledger StatementImportLedger
}

var _ StatementImporter = (*StatementImportService)(nil)

// NewStatementImportService constructs the domain service with an explicit
// ledger. A nil or typed-nil port is rejected before any statement is trusted.
func NewStatementImportService(ledger StatementImportLedger) (*StatementImportService, error) {
	if ledger == nil || isNilStatementImportLedger(ledger) {
		return nil, fmt.Errorf("%w: ledger is required", ErrStatementImportLedgerUnavailable)
	}
	return &StatementImportService{ledger: ledger}, nil
}

func isNilStatementImportLedger(ledger StatementImportLedger) bool {
	value := reflect.ValueOf(ledger)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Import validates one authenticated normalized statement, classifies each
// statement-line revision as accepted, replayed or unmatched, and appends the
// statement once. A changed payload under an existing immutable identity
// returns ErrStatementImportConflict with no durable change.
func (s *StatementImportService) Import(ctx context.Context, scope TrustedStatementScope, in economics.StatementBatch) (economics.ImportResult, error) {
	if s == nil || s.ledger == nil {
		return economics.ImportResult{}, fmt.Errorf("%w: service is not constructed", ErrStatementImportLedgerUnavailable)
	}
	if ctx == nil {
		return economics.ImportResult{}, fmt.Errorf("%w: nil context", ErrStatementImportInvalid)
	}
	if err := ctx.Err(); err != nil {
		return economics.ImportResult{}, err
	}
	normalized, err := NormalizeStatement(scope, in)
	if err != nil {
		return economics.ImportResult{}, err
	}
	return s.importNormalized(ctx, normalized)
}

func (s *StatementImportService) importNormalized(ctx context.Context, statement NormalizedStatement) (economics.ImportResult, error) {
	statementKey := statement.Identity.Key()
	retainedFingerprint, found, err := s.ledger.LookupStatementRevision(ctx, statement.Identity)
	if err != nil {
		return economics.ImportResult{}, fmt.Errorf("%w: %w", ErrStatementImportLedgerUnavailable, err)
	}
	if found && retainedFingerprint != statement.Fingerprint {
		return statementImportRejectedResult(statement), &StatementImportConflictError{StatementKey: statementKey}
	}

	result := economics.ImportResult{}
	if found {
		for _, line := range statement.Lines {
			result.Replayed = append(result.Replayed, line.Line.ID)
		}
		sortStatementImportResult(&result)
		return result, nil
	}

	identities := make([]economics.StatementLineIdentity, len(statement.Lines))
	for i, line := range statement.Lines {
		identities[i] = line.Identity
	}
	retainedLines, err := s.ledger.LookupStatementLines(ctx, identities)
	if err != nil {
		return economics.ImportResult{}, fmt.Errorf("%w: %w", ErrStatementImportLedgerUnavailable, err)
	}

	var conflicts []string
	for _, line := range statement.Lines {
		if retained, ok := retainedLines[line.Identity.Key()]; ok {
			if retained == line.Fingerprint {
				result.Replayed = append(result.Replayed, line.Line.ID)
				continue
			}
			conflicts = append(conflicts, line.Line.ID)
			continue
		}
		if line.Line.Outcome == economics.StatementLineUnmatched {
			result.Unmatched = append(result.Unmatched, line.Line.ID)
			continue
		}
		result.Accepted = append(result.Accepted, line.Line.ID)
	}
	if len(conflicts) > 0 {
		result.Rejected = conflicts
		sortStatementImportResult(&result)
		return result, &StatementImportConflictError{StatementKey: statementKey, LineIDs: slices.Clone(conflicts)}
	}

	if err := s.ledger.AppendStatementRevision(ctx, statement.Clone()); err != nil {
		if errors.Is(err, ErrStatementImportConflict) {
			return economics.ImportResult{}, err
		}
		return economics.ImportResult{}, fmt.Errorf("%w: %w", ErrStatementImportLedgerUnavailable, err)
	}
	sortStatementImportResult(&result)
	if err := result.Validate(); err != nil {
		return economics.ImportResult{}, fmt.Errorf("%w: %v", ErrStatementImportInvalid, err)
	}
	return result, nil
}

func statementImportRejectedResult(statement NormalizedStatement) economics.ImportResult {
	result := economics.ImportResult{Rejected: make([]string, 0, len(statement.Lines))}
	for _, line := range statement.Lines {
		result.Rejected = append(result.Rejected, line.Line.ID)
	}
	sortStatementImportResult(&result)
	return result
}

func sortStatementImportResult(result *economics.ImportResult) {
	slices.Sort(result.Accepted)
	slices.Sort(result.Replayed)
	slices.Sort(result.Unmatched)
	slices.Sort(result.Rejected)
}

// StatementImportConflictError reports an immutable statement or line identity
// whose submitted content differs from the retained revision. LineIDs is
// populated when a specific line revision conflicts and is empty for a
// statement-level envelope conflict. It unwraps to ErrStatementImportConflict.
type StatementImportConflictError struct {
	StatementKey string
	LineIDs      []string
}

func (e *StatementImportConflictError) Error() string {
	if e == nil {
		return ErrStatementImportConflict.Error()
	}
	if len(e.LineIDs) == 0 {
		return fmt.Sprintf("%s: statement %q", ErrStatementImportConflict, e.StatementKey)
	}
	return fmt.Sprintf("%s: statement %q lines %s", ErrStatementImportConflict, e.StatementKey, strings.Join(e.LineIDs, ","))
}

func (e *StatementImportConflictError) Unwrap() error { return ErrStatementImportConflict }
