package billing

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// Single core customer settlement authority evaluator (refinement Task 5.3,
// R12).
//
// The rolling A-leg report shell owns SQL, pagination, presence/journal
// retention bounds, and snapshot assembly. All per-call financial proof —
// marker identity, canonical journal binding, same-scope correction
// chains, stray detection, and the pass-through adjustment plane — lives
// here as one side-effect-free pure function over already-loaded facts.
// No SQL, no I/O, no provider SDKs. Unknown is never zero; only
// infrastructure failures (unparseable identity) and unrepresentable
// money fail the query.

const (
	// ALegSettlementKind is the canonical customer settlement operation kind.
	ALegSettlementKind = "customer_call_settlement"
	// ALegRepairKind is the canonical no-charge repair operation kind.
	ALegRepairKind = "customer_no_charge_repair"
)

// Cycle 1 customer-authority issue codes owned by the core evaluator.
// Retention/fanout bounds (leg/journal/entry fanout, truncation) stay with
// the report shell; they are not financial proof.
const (
	ALegIssueMarkerMissing        = "customer_marker_missing"
	ALegIssueMarkersConflicting   = "customer_markers_conflicting"
	ALegIssueMarkerKey            = "customer_marker_key"
	ALegIssueMarkerIntegrity      = "customer_marker_integrity"
	ALegIssueMarkerCurrency       = "customer_marker_currency"
	ALegIssueMarkerFingerprint    = "customer_marker_fingerprint"
	ALegIssueJournalMissing       = "customer_journal_missing"
	ALegIssueJournalMismatch      = "customer_journal_mismatch"
	ALegIssueJournalInvalid       = "customer_journal_invalid"
	ALegIssueStrayJournal         = "customer_stray_journal"
	ALegIssueCorrectionUnresolved = "customer_correction_unresolved"
	ALegIssueAdjustmentPending    = "customer_adjustment_pending"
	ALegIssueMarkerInvalid        = "customer_marker_invalid"
	ALegIssueBookConflict         = "customer_book_conflict"
)

// alegProviderJournalOperationKind names deferred provider rows. They ride
// the operator plane and never participate in customer authority.
const alegProviderJournalOperationKind = "provider_call_cogs"

// ALegAuthorityScope is the explicit queried scope threaded through every
// customer-authority decision. Authority never consults evidence outside
// these four coordinates plus the operation kind under proof.
type ALegAuthorityScope struct {
	AccountID string
	ALegID    string
	CallID    string
	Currency  string
	// StoreID scopes provider-leg facts to the serving store. Customer
	// authority ignores it; provider authority requires exact agreement.
	StoreID string
}

// ALegMarker is one durable operation snapshot fact for the call under
// proof, already loaded inside the report snapshot transaction.
type ALegMarker struct {
	OperationKey         string
	AccountID            string
	OperationKind        string
	SourceKey            string
	Fingerprint          string
	IntegrityFingerprint string
	Currency             string
	Mode                 string
	Before               AccountSnapshot
	After                AccountSnapshot
	SequenceStart        uint64
	SequenceEnd          uint64
}

// ALegCallVerdict is the per-call customer authority verdict for one snapshot.
type ALegCallVerdict struct {
	Status      ALegCallStatus
	Charge      Money
	Known       bool
	OpKey       string
	Adjustments []ALegAdjustmentRef
}

// errALegMarkerSemantics marks selected-marker semantic failures for the
// marker_invalid issue; canonical root failures wrap ErrJournalInvalid.
var errALegMarkerSemantics = errors.New("billing: marker semantics")

// EvaluateALegCallAuthority proves one call's customer authority inside the
// snapshot. It returns the verdict plus any issues; only infrastructure
// failures and unrepresentable money fail the query.
func EvaluateALegCallAuthority(scope ALegAuthorityScope, markers []ALegMarker, journals []JournalTransaction) (ALegCallVerdict, []ReconciliationIssue, error) {
	currency := scope.Currency
	callID := scope.CallID
	issue := func(code string, sequence uint64) ReconciliationIssue {
		return ReconciliationIssue{Code: code, Sequence: sequence, Detail: callID}
	}
	unknown := func(code string, sequence uint64) (ALegCallVerdict, []ReconciliationIssue, error) {
		return ALegCallVerdict{Status: ALegCallUnknown, Charge: Money{Currency: currency}}, []ReconciliationIssue{issue(code, sequence)}, nil
	}
	if len(markers) == 0 {
		return ALegCallVerdict{Status: ALegCallPending, Charge: Money{Currency: currency}},
			[]ReconciliationIssue{issue(ALegIssueMarkerMissing, 0)}, nil
	}
	settlement, repair := -1, -1
	for i, marker := range markers {
		switch marker.OperationKind {
		case ALegSettlementKind:
			if settlement >= 0 {
				return unknown(ALegIssueMarkersConflicting, marker.SequenceEnd)
			}
			settlement = i
		case ALegRepairKind:
			if repair >= 0 {
				return unknown(ALegIssueMarkersConflicting, marker.SequenceEnd)
			}
			repair = i
		default:
			return unknown(ALegIssueMarkersConflicting, marker.SequenceEnd)
		}
	}
	if settlement >= 0 && repair >= 0 {
		return unknown(ALegIssueMarkersConflicting, markers[settlement].SequenceEnd)
	}
	if settlement < 0 && repair < 0 {
		return unknown(ALegIssueMarkersConflicting, markers[0].SequenceEnd)
	}
	marker := markers[settlement]
	kind := ALegSettlementKind
	if settlement < 0 {
		marker = markers[repair]
		kind = ALegRepairKind
	}
	call, err := ParseBillingCallID(callID)
	if err != nil {
		return ALegCallVerdict{}, nil, fmt.Errorf("billing: A-leg call identity: %w", err)
	}
	source, err := CustomerSettlementSourceKey(scope.AccountID, call)
	if err != nil {
		return ALegCallVerdict{}, nil, fmt.Errorf("billing: A-leg settlement source: %w", err)
	}
	// Exact canonical marker identity: raw byte-exact key, kind, source,
	// and native currency per the settlement writer contract.
	if marker.OperationKey != source+":"+kind || marker.OperationKind != kind || marker.SourceKey != callID {
		return unknown(ALegIssueMarkerKey, marker.SequenceEnd)
	}
	if marker.AccountID != scope.AccountID {
		return unknown(ALegIssueMarkerKey, marker.SequenceEnd)
	}
	want := alegMarkerIntegrity(marker.OperationKey, marker.AccountID, marker.OperationKind, marker.SourceKey, marker.Fingerprint, marker.Before, marker.After, marker.SequenceStart, marker.SequenceEnd)
	if marker.IntegrityFingerprint == "" || marker.IntegrityFingerprint != want {
		return unknown(ALegIssueMarkerIntegrity, marker.SequenceEnd)
	}
	if marker.Currency != currency {
		return unknown(ALegIssueMarkerCurrency, marker.SequenceEnd)
	}
	if marker.Fingerprint == "" {
		return unknown(ALegIssueMarkerFingerprint, marker.SequenceEnd)
	}
	// Only exact supported financial-book rows may establish authority.
	for i := range journals {
		journal := &journals[i]
		if (journal.OperationKind == ALegSettlementKind || journal.OperationKind == ALegRepairKind ||
			journal.OperationKind == CostPassThroughAdjustmentOperationKind) &&
			journal.Book != JournalBookFinancial {
			return unknown(ALegIssueBookConflict, journal.AccountSequence)
		}
	}
	var canonical *JournalTransaction
	byID := make(map[string]*JournalTransaction, len(journals))
	for i := range journals {
		journal := &journals[i]
		byID[journal.ID] = journal
		if journal.OperationKind == ALegSettlementKind || journal.OperationKind == ALegRepairKind {
			if journal.ID == source && journal.OperationKind == kind && journal.TurnID == callID {
				if canonical != nil {
					return unknown(ALegIssueJournalMismatch, journal.AccountSequence)
				}
				canonical = journal
			}
		}
	}
	if err := proveALegOperationPlane(marker, canonical); err != nil {
		if errors.Is(err, errALegMarkerSemantics) {
			return unknown(ALegIssueMarkerInvalid, marker.SequenceEnd)
		}
		seq := marker.SequenceEnd
		if canonical != nil {
			seq = canonical.AccountSequence
		}
		return unknown(ALegIssueJournalMismatch, seq)
	}
	if canonical != nil {
		if kind == ALegRepairKind {
			return unknown(ALegIssueJournalMismatch, canonical.AccountSequence)
		}
		if err := proveALegCanonicalJournal(scope, marker, canonical, source); err != nil {
			if errors.Is(err, ErrJournalFingerprint) {
				return unknown(ALegIssueJournalInvalid, canonical.AccountSequence)
			}
			return unknown(ALegIssueJournalMismatch, canonical.AccountSequence)
		}
	} else {
		if marker.Before.BalanceNano != marker.After.BalanceNano || marker.Before.SpendableNano != marker.After.SpendableNano ||
			marker.Before.Version != marker.After.Version || marker.SequenceStart != marker.SequenceEnd {
			return unknown(ALegIssueJournalMissing, marker.SequenceEnd)
		}
	}
	// Canonical-rooted deterministic correction graph: every linked
	// claim must be exactly-one-linked, shape-exact, and transitively
	// reachable from the correction-free canonical root. Non-root
	// components, cycles, duplicate/competing links, ambiguous heads,
	// and malformed shapes resolve the call unknown with no partial
	// netting.
	admitted, admitSeq, admittedOK := admitALegCorrections(scope, kind, canonical, journals, byID)
	if !admittedOK {
		return unknown(ALegIssueCorrectionUnresolved, admitSeq)
	}
	referenced := make(map[string]bool, len(admitted))
	for _, claim := range admitted {
		if claim.ReversalOf != "" {
			referenced[claim.ReversalOf] = true
		} else {
			referenced[claim.CorrectsTransactionID] = true
		}
	}
	debitSum, creditSum := int64(0), int64(0)
	if canonical != nil {
		debitSum = canonical.Entries[0].Amount.Nano
	}
	for _, claim := range admitted {
		for _, entry := range claim.Entries {
			if entry.LedgerAccount != "customer_financial_account" {
				continue
			}
			if entry.Side == JournalDebit {
				debitSum, err = AddReportAmount(debitSum, entry.Amount, currency)
			} else {
				creditSum, err = AddReportAmount(creditSum, entry.Amount, currency)
			}
			if err != nil {
				return ALegCallVerdict{}, nil, fmt.Errorf("%w: A-leg correction net: %v", ErrMoneyOverflow, err)
			}
		}
	}
	for i := range journals {
		journal := &journals[i]
		if journal == canonical || referenced[journal.ID] {
			continue
		}
		if journal.Book != JournalBookFinancial {
			continue
		}
		switch journal.OperationKind {
		case ALegSettlementKind, ALegRepairKind:
			if journal.ReversalOf == "" && journal.CorrectsTransactionID == "" {
				return unknown(ALegIssueStrayJournal, journal.AccountSequence)
			}
		case CostPassThroughAdjustmentOperationKind:
			continue
		case alegProviderJournalOperationKind:
			continue
		default:
			return unknown(ALegIssueStrayJournal, journal.AccountSequence)
		}
	}
	net, err := ReportDifference(currency, debitSum, creditSum)
	if err != nil {
		return ALegCallVerdict{}, nil, fmt.Errorf("%w: A-leg call net: %v", ErrMoneyOverflow, err)
	}
	if canonical != nil && net.Nano < 0 {
		return unknown(ALegIssueJournalMismatch, canonical.AccountSequence)
	}
	// Cycle 1: any pass-through evidence defers the call to pending.
	var adjustments []ALegAdjustmentRef
	hasAdjustment := false
	for i := range journals {
		journal := &journals[i]
		if journal.OperationKind != CostPassThroughAdjustmentOperationKind || journal.TurnID != callID {
			continue
		}
		hasAdjustment = true
		var pairNet int64
		for _, entry := range journal.Entries {
			if entry.LedgerAccount != "customer_financial_account" || entry.Amount.Currency != currency {
				continue
			}
			if entry.Side == JournalDebit {
				pairNet, err = AddReportAmount(pairNet, entry.Amount, currency)
				if err != nil {
					return ALegCallVerdict{}, nil, fmt.Errorf("%w: A-leg adjustment net: %v", ErrMoneyOverflow, err)
				}
			} else {
				var diff Money
				diff, err = ReportDifference(currency, pairNet, entry.Amount.Nano)
				if err != nil {
					return ALegCallVerdict{}, nil, fmt.Errorf("%w: A-leg adjustment net: %v", ErrMoneyOverflow, err)
				}
				pairNet = diff.Nano
			}
		}
		amount, err := ReportDifference(currency, 0, pairNet)
		if err != nil {
			continue
		}
		adjustments = append(adjustments, ALegAdjustmentRef{TransactionID: journal.ID, Amount: amount})
	}
	if hasAdjustment {
		class := ALegCallVerdict{Status: ALegCallPending, Charge: Money{Currency: currency}, Adjustments: adjustments}
		return class, []ReconciliationIssue{issue(ALegIssueAdjustmentPending, 0)}, nil
	}
	charge := Money{Nano: net.Nano, Currency: currency}
	return ALegCallVerdict{Status: ALegCallKnown, Charge: charge, Known: true, OpKey: marker.OperationKey, Adjustments: adjustments}, nil, nil
}

// proveALegOperationPlane validates the selected marker/canonical
// operation-plane invariants once: marker semantic coherence beyond digest
// integrity plus canonical root purity.
func proveALegOperationPlane(marker ALegMarker, canonical *JournalTransaction) error {
	mode := AccountMode(marker.Mode)
	if mode != AccountPrepaid && mode != AccountPostpaid {
		return fmt.Errorf("%w: marker %q mode %q is not a valid account mode", errALegMarkerSemantics, marker.OperationKey, marker.Mode)
	}
	if marker.SequenceStart > marker.SequenceEnd {
		return fmt.Errorf("%w: marker %q sequence range runs backward", errALegMarkerSemantics, marker.OperationKey)
	}
	if marker.After.Version < marker.Before.Version {
		return fmt.Errorf("%w: marker %q version transition runs backward", errALegMarkerSemantics, marker.OperationKey)
	}
	if canonical != nil && (canonical.ReversalOf != "" || canonical.CorrectsTransactionID != "" || canonical.CorrectionGroupID != "") {
		return fmt.Errorf("%w: canonical journal %q carries correction linkage", ErrJournalInvalid, canonical.ID)
	}
	return nil
}

// proveALegCanonicalJournal binds a proven marker to its canonical journal
// inside the queried scope: sealed fingerprint re-verified, exact writer
// identity and shape, identical snapshots, and a checked positive delta.
func proveALegCanonicalJournal(scope ALegAuthorityScope, marker ALegMarker, canonical *JournalTransaction, expectedSource string) error {
	if err := canonical.Validate(); err != nil {
		return err
	}
	fingerprint, err := canonical.CanonicalFingerprint()
	if err != nil {
		return err
	}
	if canonical.SemanticFingerprint != fingerprint {
		return fmt.Errorf("%w: canonical journal %q stored fingerprint does not match sealed content", ErrJournalFingerprint, canonical.ID)
	}
	if canonical.SourceKey != expectedSource {
		return fmt.Errorf("%w: canonical journal %q source mismatch", ErrJournalInvalid, canonical.ID)
	}
	if canonical.AccountID != scope.AccountID || canonical.TurnID != scope.CallID ||
		canonical.ALegID != scope.ALegID || canonical.BLegID != "" ||
		canonical.Currency != scope.Currency || canonical.OperationKind != marker.OperationKind {
		return fmt.Errorf("%w: canonical journal %q is outside report scope", ErrJournalInvalid, canonical.ID)
	}
	journalBefore := AccountSnapshot{
		BalanceNano: canonical.BalanceBefore, SpendableNano: canonical.SpendableBefore,
		CreditFloorNano: canonical.CreditFloor, CreditLimitNano: canonical.CreditLimit,
		Mode: AccountMode(canonical.Mode), Currency: canonical.Currency, Version: canonical.SnapshotVersionBefore,
	}
	journalAfter := AccountSnapshot{
		BalanceNano: canonical.BalanceAfter, SpendableNano: canonical.SpendableAfter,
		CreditFloorNano: canonical.CreditFloor, CreditLimitNano: canonical.CreditLimit,
		Mode: AccountMode(canonical.Mode), Currency: canonical.Currency, Version: canonical.SnapshotVersionAfter,
	}
	if !reflect.DeepEqual(marker.Before, journalBefore) || !reflect.DeepEqual(marker.After, journalAfter) {
		return fmt.Errorf("%w: marker %q snapshots disagree with canonical journal %q", ErrJournalInvalid, marker.OperationKey, canonical.ID)
	}
	if len(canonical.Entries) != 2 {
		return fmt.Errorf("%w: canonical journal %q must carry exactly the writer pair", ErrJournalInvalid, canonical.ID)
	}
	first, second := canonical.Entries[0], canonical.Entries[1]
	if first.LedgerAccount != "customer_financial_account" || first.Side != JournalDebit ||
		second.LedgerAccount != "usage_revenue" || second.Side != JournalCredit {
		return fmt.Errorf("%w: canonical journal %q must carry exactly the writer pair", ErrJournalInvalid, canonical.ID)
	}
	if first.Amount.Currency != scope.Currency || second.Amount.Currency != scope.Currency {
		return fmt.Errorf("%w: canonical journal %q entries must use report currency", ErrJournalInvalid, canonical.ID)
	}
	delta, err := ReportDifference(scope.Currency, marker.Before.BalanceNano, marker.After.BalanceNano)
	if err != nil || delta.Nano <= 0 {
		return fmt.Errorf("%w: marker %q has no positive account delta", ErrJournalInvalid, marker.OperationKey)
	}
	spendable, err := ReportDifference(scope.Currency, marker.Before.SpendableNano, marker.After.SpendableNano)
	if err != nil || spendable.Nano != delta.Nano {
		return fmt.Errorf("%w: marker %q spendable delta disagrees with balance delta", ErrJournalInvalid, marker.OperationKey)
	}
	if first.Amount.Nano != delta.Nano || second.Amount.Nano != delta.Nano {
		return fmt.Errorf("%w: marker %q amounts disagree with account delta", ErrJournalInvalid, marker.OperationKey)
	}
	return nil
}

// admitALegCorrections builds the deterministic correction graph for the
// selected marker operation plane. Candidates are non-canonical
// settlement/repair journals carrying linkage, ordered by
// (account_sequence, transaction_id) so every outcome is input-order
// independent. Phase A proves each claim locally (single link kind,
// scope/plane/fingerprint/group, exact ledger shape). Phase B rejects
// duplicate or competing claims on one raw-exact target. Phase C admits
// only claims transitively reachable from the correction-free canonical
// root, keeping the replacement-needs-reversal rule. Any failure returns
// the deterministic offending sequence for customer_correction_unresolved.
func admitALegCorrections(scope ALegAuthorityScope, kind string, canonical *JournalTransaction, journals []JournalTransaction, byID map[string]*JournalTransaction) (admitted []*JournalTransaction, failSeq uint64, ok bool) {
	var candidates []*JournalTransaction
	for i := range journals {
		journal := &journals[i]
		if journal == canonical {
			continue
		}
		if journal.OperationKind != ALegSettlementKind && journal.OperationKind != ALegRepairKind {
			continue
		}
		if journal.ReversalOf == "" && journal.CorrectsTransactionID == "" {
			continue
		}
		candidates = append(candidates, journal)
	}
	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].AccountSequence != candidates[b].AccountSequence {
			return candidates[a].AccountSequence < candidates[b].AccountSequence
		}
		return candidates[a].ID < candidates[b].ID
	})
	for _, claim := range candidates {
		// Corrections adjust the canonical transaction; they never
		// independently establish economics.
		if canonical == nil {
			return nil, claim.AccountSequence, false
		}
		if err := proveALegCorrectionLink(scope, kind, claim, byID); err != nil {
			return nil, claim.AccountSequence, false
		}
		if err := checkALegCorrectionShape(claim); err != nil {
			return nil, claim.AccountSequence, false
		}
	}
	reversedBy := make(map[string]*JournalTransaction, len(candidates))
	correctedBy := make(map[string]*JournalTransaction, len(candidates))
	for _, claim := range candidates {
		if claim.ReversalOf != "" {
			if _, dup := reversedBy[claim.ReversalOf]; dup {
				return nil, claim.AccountSequence, false
			}
			reversedBy[claim.ReversalOf] = claim
			continue
		}
		if _, dup := correctedBy[claim.CorrectsTransactionID]; dup {
			return nil, claim.AccountSequence, false
		}
		correctedBy[claim.CorrectsTransactionID] = claim
	}
	reachable := map[string]bool{}
	if canonical != nil {
		reachable[canonical.ID] = true
	}
	admittedIDs := make(map[string]bool, len(candidates))
	remaining := append([]*JournalTransaction(nil), candidates...)
	for len(remaining) > 0 {
		progress := false
		var rest []*JournalTransaction
		for _, claim := range remaining {
			target := claim.ReversalOf
			if target == "" {
				target = claim.CorrectsTransactionID
			}
			if !reachable[target] {
				rest = append(rest, claim)
				continue
			}
			if claim.CorrectsTransactionID != "" {
				rev, ok := reversedBy[claim.CorrectsTransactionID]
				if !ok || !admittedIDs[rev.ID] {
					rest = append(rest, claim)
					continue
				}
			}
			admitted = append(admitted, claim)
			admittedIDs[claim.ID] = true
			reachable[claim.ID] = true
			progress = true
		}
		if !progress {
			return nil, rest[0].AccountSequence, false
		}
		remaining = rest
	}
	return admitted, 0, true
}

// checkALegCorrectionShape enforces the exact trusted customer settlement
// correction journal shape before netting: exactly the canonical writer
// pair ledgers (customer_financial_account plus usage_revenue, in either
// balanced orientation), no unrelated clearing account, no duplicate
// plane, no extra entries. It mirrors the canonical writer contract
// (customer debit plus usage-revenue credit) and its reversal mirror;
// the generic balanced-journal validator alone cannot establish this.
func checkALegCorrectionShape(journal *JournalTransaction) error {
	if len(journal.Entries) != 2 {
		return fmt.Errorf("%w: correction %q must carry exactly the writer pair", ErrJournalInvalid, journal.ID)
	}
	first, second := journal.Entries[0], journal.Entries[1]
	customer := func(entry JournalEntry, side JournalSide) bool {
		return entry.LedgerAccount == "customer_financial_account" && entry.Side == side
	}
	revenue := func(entry JournalEntry, side JournalSide) bool {
		return entry.LedgerAccount == "usage_revenue" && entry.Side == side
	}
	forward := customer(first, JournalDebit) && revenue(second, JournalCredit) ||
		customer(second, JournalDebit) && revenue(first, JournalCredit)
	mirror := customer(first, JournalCredit) && revenue(second, JournalDebit) ||
		customer(second, JournalCredit) && revenue(first, JournalDebit)
	if !forward && !mirror {
		return fmt.Errorf("%w: correction %q carries a non-canonical ledger shape", ErrJournalInvalid, journal.ID)
	}
	return nil
}

// proveALegCorrectionLink validates one correction claim against the exact
// same-scope immutable chain anchored to the queried report scope: the
// claim must carry exactly one raw-exact link kind, and the claim itself
// and every referenced target must carry identical account, call, A-leg,
// empty B-leg, native currency, and operation plane; the correction group
// must bind; and a Corrects-only replacement must have its target reversed
// elsewhere in scope. Raw-exact IDs throughout: padded references never
// resolve. It mirrors reconcile.go read-only.
func proveALegCorrectionLink(scope ALegAuthorityScope, kind string, journal *JournalTransaction, byID map[string]*JournalTransaction) error {
	if (journal.ReversalOf != "") == (journal.CorrectsTransactionID != "") {
		return fmt.Errorf("%w: correction %q must carry exactly one link kind", ErrJournalInvalid, journal.ID)
	}
	if journal.OperationKind != kind {
		return fmt.Errorf("%w: correction %q operation %q is outside the selected marker plane", ErrJournalInvalid, journal.ID, journal.OperationKind)
	}
	if err := journal.Validate(); err != nil {
		return err
	}
	fingerprint, err := journal.CanonicalFingerprint()
	if err != nil {
		return err
	}
	if journal.SemanticFingerprint != fingerprint {
		return fmt.Errorf("%w: correction %q stored fingerprint does not match sealed content", ErrJournalFingerprint, journal.ID)
	}
	if journal.AccountID != scope.AccountID || journal.TurnID != scope.CallID ||
		journal.ALegID != scope.ALegID || journal.BLegID != "" ||
		journal.Currency != scope.Currency {
		return fmt.Errorf("%w: correction %q is outside report scope", ErrJournalInvalid, journal.ID)
	}
	for _, targetID := range []string{journal.ReversalOf, journal.CorrectsTransactionID} {
		if targetID == "" {
			continue
		}
		target, ok := byID[targetID]
		if !ok {
			return fmt.Errorf("%w: correction %q target %q is missing", ErrJournalInvalid, journal.ID, targetID)
		}
		if target.ID == journal.ID || target.AccountID != scope.AccountID || target.TurnID != scope.CallID ||
			target.ALegID != scope.ALegID || target.BLegID != "" || target.Currency != scope.Currency ||
			target.OperationKind != journal.OperationKind {
			return fmt.Errorf("%w: correction %q target %q is outside scope", ErrJournalInvalid, journal.ID, targetID)
		}
		if err := target.Validate(); err != nil {
			return err
		}
		targetFingerprint, err := target.CanonicalFingerprint()
		if err != nil {
			return err
		}
		if target.SemanticFingerprint != targetFingerprint {
			return fmt.Errorf("%w: correction target %q stored fingerprint does not match sealed content", ErrJournalFingerprint, targetID)
		}
		group := target.CorrectionGroupID
		if group == "" {
			group = target.ID
		}
		if journal.CorrectionGroupID == "" || journal.CorrectionGroupID != group {
			return fmt.Errorf("%w: correction %q group mismatch for target %q", ErrJournalInvalid, journal.ID, targetID)
		}
	}
	if journal.CorrectsTransactionID != "" && journal.ReversalOf == "" {
		reversed := false
		for _, other := range byID {
			if other.ID != journal.ID && other.ReversalOf == journal.CorrectsTransactionID {
				reversed = true
				break
			}
		}
		if !reversed {
			return fmt.Errorf("%w: correction %q target %q has no reversal", ErrJournalInvalid, journal.ID, journal.CorrectsTransactionID)
		}
	}
	return nil
}

// alegMarkerIntegrity recomputes the current writer integrity digest for one
// marker. Legacy forms are rejected by inequality at the call site.
func alegMarkerIntegrity(operationKey, accountID, operationKind, sourceKey, fingerprint string, before, after AccountSnapshot, sequenceStart, sequenceEnd uint64) string {
	payload, _ := json.Marshal(struct {
		Version                                                        string
		OperationKey, AccountID, OperationKind, SourceKey, Fingerprint string
		Before, After                                                  AccountSnapshot
		SequenceStart, SequenceEnd                                     uint64
	}{"snapshot:v1", operationKey, accountID, operationKind, sourceKey, fingerprint, before, after, sequenceStart, sequenceEnd})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("snapshot:v1:%x", digest[:])
}
