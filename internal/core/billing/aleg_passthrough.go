package billing

import (
	"fmt"
	"sort"
)

// Trusted pass-through adjustment authority (refinement Task 5.3, Cycle 2).
//
// The rolling A-leg report shell owns SQL, pagination, presence/journal
// retention bounds, and snapshot assembly. All per-call pass-through
// financial proof — head identity, provider revision continuity, exact
// operation/snapshot/journal lineage, legacy empty-A-leg joins, and
// telescoping amount agreement — lives here as one side-effect-free pure
// function over already-loaded facts. No SQL, no I/O, no provider SDKs.
//
// Pass-through stays a distinct customer adjustment plane: validated
// lineage never enters retail totals. A call may report known only when
// the retail evaluator already proved canonical authority AND this
// evaluator proves complete pass-through authority; anything incomplete
// stays pending and anything conflicting stays unknown with an explicit
// issue. Unknown is never zero and malformed evidence is never partially
// netted. Only infrastructure failures (unparseable identity) and
// unrepresentable money fail the query.

// ALegPassThroughStatus is the per-call pass-through plane state. None
// means no pass-through involvement; pending means no proof yet;
// known means complete validated authority; unknown means conflicting
// evidence with an attached issue.
type ALegPassThroughStatus string

const (
	ALegPassThroughNone    ALegPassThroughStatus = "none"
	ALegPassThroughPending ALegPassThroughStatus = "pending"
	ALegPassThroughKnown   ALegPassThroughStatus = "known"
	ALegPassThroughUnknown ALegPassThroughStatus = "unknown"
)

// ALegIssueAdjustmentUnresolved marks conflicting pass-through evidence:
// the call keeps pending/unknown status and validated lineage is withheld.
const ALegIssueAdjustmentUnresolved = "customer_adjustment_unresolved"

// ALegPassThroughHead is one trusted durable head fact for the call under
// proof, already loaded inside the report snapshot transaction.
type ALegPassThroughHead struct {
	AccountID              string
	CallID                 string
	ALegID                 string
	SettlementOperationKey string
	OriginalTransactionID  string
	PolicyRef              VersionRef
	MissingCost            CostPassThroughMissingCostPolicy
	SafeBound              Money
	AllowLateAdjustment    bool
	Status                 CostPassThroughSettlementStatus
	PostedAmount           Money
	ProviderLURKey         string
	ProviderValuationID    string
	ProviderRevision       uint64
	ProviderInputHash      string
	HeadVersion            uint64
	Fence                  uint64
	SettlementFingerprint  string
}

// ALegPassThroughSnapshot is one canonical adjustment operation snapshot
// fact for the call under proof, already loaded inside the report
// snapshot transaction.
type ALegPassThroughSnapshot struct {
	OperationKey         string
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

// ALegPassThroughFacts bundles every pass-through fact the shell loads
// for one retail-known call: the trusted head (nil when absent), the
// expected charge policy from the call exposure, the selected settlement
// marker carrying the telescoping base snapshots, the canonical
// adjustment operation snapshots, and the candidate adjustment journals.
// Journals for other calls are ignored; the shell partitions by call.
type ALegPassThroughFacts struct {
	Head      *ALegPassThroughHead
	Policy    VersionRef
	Marker    ALegMarker
	HasMarker bool
	Snapshots []ALegPassThroughSnapshot
	Journals  []JournalTransaction
}

// ALegPassThroughVerdict is the per-call pass-through authority verdict
// for one snapshot. Adjustments carry validated lineage only when Status
// is known; otherwise they are lineage-only evidence with Validated
// false or withheld entirely.
type ALegPassThroughVerdict struct {
	Status       ALegPassThroughStatus
	PostedAmount Money
	Revision     uint64
	Adjustments  []ALegAdjustmentRef
}

// EvaluateALegPassThroughAuthority proves one call's pass-through plane
// inside the snapshot. It returns the verdict plus any issues; only
// infrastructure failures and unrepresentable money fail the query.
func EvaluateALegPassThroughAuthority(scope ALegAuthorityScope, facts ALegPassThroughFacts) (ALegPassThroughVerdict, []ReconciliationIssue, error) {
	currency := scope.Currency
	callID := scope.CallID
	issue := func(code string, sequence uint64) ReconciliationIssue {
		return ReconciliationIssue{Code: code, Sequence: sequence, Detail: callID}
	}
	unknown := func(code string, sequence uint64) (ALegPassThroughVerdict, []ReconciliationIssue, error) {
		return ALegPassThroughVerdict{Status: ALegPassThroughUnknown}, []ReconciliationIssue{issue(code, sequence)}, nil
	}
	var journals []*JournalTransaction
	for i := range facts.Journals {
		journal := &facts.Journals[i]
		if journal.OperationKind != CostPassThroughAdjustmentOperationKind || journal.TurnID != callID {
			continue
		}
		journals = append(journals, journal)
	}
	if facts.Head == nil {
		// Cycle 1 compatible: unresolved lineage defers without proof.
		// Any book conflict still fails closed before netting.
		for _, journal := range journals {
			if journal.Book != JournalBookFinancial {
				return unknown(ALegIssueBookConflict, journal.AccountSequence)
			}
		}
		if len(journals) == 0 {
			return ALegPassThroughVerdict{Status: ALegPassThroughNone}, nil, nil
		}
		adjustments, err := listALegUnresolvedAdjustments(currency, journals)
		if err != nil {
			return ALegPassThroughVerdict{}, nil, err
		}
		return ALegPassThroughVerdict{Status: ALegPassThroughPending, Adjustments: adjustments},
			[]ReconciliationIssue{issue(ALegIssueAdjustmentPending, 0)}, nil
	}
	head := facts.Head
	if head.AccountID != scope.AccountID || head.CallID != scope.CallID || head.ALegID != scope.ALegID {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if head.SafeBound.Currency != currency || head.PostedAmount.Currency != currency {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if head.PolicyRef != facts.Policy {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if facts.Policy.ID == "" || facts.Policy.Version == "" {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if !facts.HasMarker || facts.Marker.Fingerprint == "" ||
		head.SettlementFingerprint == "" || head.SettlementFingerprint != facts.Marker.Fingerprint {
		return unknown(ALegIssueAdjustmentUnresolved, facts.Marker.SequenceEnd)
	}
	if head.HeadVersion == 0 || head.Fence == 0 || head.HeadVersion != head.Fence {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if head.MissingCost != CostPassThroughMissingCostPending && head.MissingCost != CostPassThroughMissingCostProvisional {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if head.Status == CostPassThroughSettlementProvisional && !head.AllowLateAdjustment {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if head.PostedAmount.Nano < 0 || head.PostedAmount.Nano > head.SafeBound.Nano {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	switch head.Status {
	case CostPassThroughSettlementPending, CostPassThroughSettlementProvisional, CostPassThroughSettlementFinal:
	default:
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if head.Status == CostPassThroughSettlementFinal && head.ProviderRevision == 0 {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	for _, journal := range journals {
		if journal.Book != JournalBookFinancial {
			return unknown(ALegIssueBookConflict, journal.AccountSequence)
		}
	}
	var provider CostPassThroughProviderCost
	if head.ProviderRevision > 0 {
		provider = CostPassThroughProviderCost{
			LURKey: head.ProviderLURKey, ValuationID: head.ProviderValuationID,
			Revision: head.ProviderRevision, InputHash: head.ProviderInputHash,
			Amount: head.PostedAmount, AmountPresent: true, Reconciled: true, Authoritative: true,
		}
		if err := provider.Validate(currency); err != nil {
			return unknown(ALegIssueAdjustmentUnresolved, 0)
		}
	}
	initial, err := checkedSub(facts.Marker.Before.BalanceNano, facts.Marker.After.BalanceNano)
	if err != nil {
		return ALegPassThroughVerdict{}, nil, fmt.Errorf("%w: A-leg pass-through base: %v", ErrMoneyOverflow, err)
	}
	sort.Slice(journals, func(a, b int) bool {
		if journals[a].AccountSequence != journals[b].AccountSequence {
			return journals[a].AccountSequence < journals[b].AccountSequence
		}
		return journals[a].ID < journals[b].ID
	})
	bySource := make(map[string]*JournalTransaction, len(journals))
	byRevision := make(map[uint64]bool, len(journals))
	type admittedClaim struct {
		journal JournalTransaction
		delta   int64
	}
	var admitted []admittedClaim
	var refs []ALegAdjustmentRef
	for _, journal := range journals {
		if _, dup := bySource[journal.SourceKey]; dup {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		parsedAccount, parsedCall, parsedLUR, parsedRevision, parsedValuation, parseErr := ParseCostPassThroughAdjustmentSourceKey(journal.SourceKey)
		if parseErr != nil {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		if parsedAccount != scope.AccountID || parsedCall != scope.CallID || parsedLUR != head.ProviderLURKey {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		if parsedRevision == 0 || parsedRevision > head.ProviderRevision {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		if byRevision[parsedRevision] {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		bySource[journal.SourceKey] = journal
		byRevision[parsedRevision] = true
		if journal.Currency != currency {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		if err := journal.Validate(); err != nil {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		fingerprint, err := journal.CanonicalFingerprint()
		if err != nil {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		if journal.SemanticFingerprint != fingerprint {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		// Current journals use exact A-leg lineage. Legacy empty-A-leg
		// journals join only through this trusted head: it names the
		// queried scope exactly and the source lineage below already
		// binds account, call, and provider continuity.
		if journal.ALegID != scope.ALegID && journal.ALegID != "" {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		if err := checkALegPassThroughShape(journal); err != nil {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		group := head.OriginalTransactionID
		if group == "" {
			group = head.SettlementOperationKey
		}
		if journal.CorrectionGroupID == "" || journal.CorrectionGroupID != group {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		var snapshot *ALegPassThroughSnapshot
		for i := range facts.Snapshots {
			candidate := &facts.Snapshots[i]
			if candidate.SourceKey != journal.SourceKey {
				continue
			}
			if snapshot != nil {
				return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
			}
			snapshot = candidate
		}
		if snapshot == nil {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		if err := checkALegPassThroughSnapshot(scope, journal, snapshot); err != nil {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		if parsedRevision == head.ProviderRevision && parsedValuation != head.ProviderValuationID {
			return unknown(ALegIssueAdjustmentUnresolved, journal.AccountSequence)
		}
		delta, err := signedALegPassThroughDelta(currency, journal)
		if err != nil {
			return ALegPassThroughVerdict{}, nil, err
		}
		admitted = append(admitted, admittedClaim{journal: *journal, delta: delta})
		negated, err := ReportDifference(currency, 0, delta)
		if err != nil {
			return ALegPassThroughVerdict{}, nil, fmt.Errorf("%w: A-leg pass-through lineage: %v", ErrMoneyOverflow, err)
		}
		refs = append(refs, ALegAdjustmentRef{TransactionID: journal.ID, Amount: negated, Validated: true})
	}
	running := initial
	for _, claim := range admitted {
		running, err = checkedAdd(running, claim.delta)
		if err != nil {
			return ALegPassThroughVerdict{}, nil, fmt.Errorf("%w: A-leg pass-through net: %v", ErrMoneyOverflow, err)
		}
	}
	if running != head.PostedAmount.Nano {
		return unknown(ALegIssueAdjustmentUnresolved, 0)
	}
	if head.Status != CostPassThroughSettlementFinal {
		for i := range refs {
			refs[i].Validated = false
		}
		return ALegPassThroughVerdict{Status: ALegPassThroughPending, Adjustments: refs},
			[]ReconciliationIssue{issue(ALegIssueAdjustmentPending, 0)}, nil
	}
	return ALegPassThroughVerdict{
		Status: ALegPassThroughKnown, PostedAmount: head.PostedAmount,
		Revision: head.ProviderRevision, Adjustments: refs,
	}, nil, nil
}

// listALegUnresolvedAdjustments reproduces the Cycle 1 lineage-only
// listing: per-journal checked financial-side netting with
// unrepresentable amounts skipped, never wrapped.
func listALegUnresolvedAdjustments(currency string, journals []*JournalTransaction) ([]ALegAdjustmentRef, error) {
	var adjustments []ALegAdjustmentRef
	for _, journal := range journals {
		var pairNet int64
		var err error
		for _, entry := range journal.Entries {
			if entry.LedgerAccount != "customer_financial_account" || entry.Amount.Currency != currency {
				continue
			}
			if entry.Side == JournalDebit {
				pairNet, err = AddReportAmount(pairNet, entry.Amount, currency)
				if err != nil {
					return nil, fmt.Errorf("%w: A-leg adjustment net: %v", ErrMoneyOverflow, err)
				}
			} else {
				var diff Money
				diff, err = ReportDifference(currency, pairNet, entry.Amount.Nano)
				if err != nil {
					return nil, fmt.Errorf("%w: A-leg adjustment net: %v", ErrMoneyOverflow, err)
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
	return adjustments, nil
}

// checkALegPassThroughShape enforces the exact trusted adjustment writer
// shape: exactly the writer pair ledgers (customer_financial_account
// plus customer_adjustment_clearing, in either balanced orientation),
// no unrelated clearing account, no extra entries.
func checkALegPassThroughShape(journal *JournalTransaction) error {
	if len(journal.Entries) != 2 {
		return fmt.Errorf("%w: adjustment %q must carry exactly the writer pair", ErrJournalInvalid, journal.ID)
	}
	first, second := journal.Entries[0], journal.Entries[1]
	financial := func(entry JournalEntry, side JournalSide) bool {
		return entry.LedgerAccount == "customer_financial_account" && entry.Side == side
	}
	clearing := func(entry JournalEntry, side JournalSide) bool {
		return entry.LedgerAccount == "customer_adjustment_clearing" && entry.Side == side
	}
	forward := financial(first, JournalDebit) && clearing(second, JournalCredit) ||
		financial(second, JournalDebit) && clearing(first, JournalCredit)
	mirror := financial(first, JournalCredit) && clearing(second, JournalDebit) ||
		financial(second, JournalCredit) && clearing(first, JournalDebit)
	if !forward && !mirror {
		return fmt.Errorf("%w: adjustment %q carries a non-canonical ledger shape", ErrJournalInvalid, journal.ID)
	}
	if first.Amount.Nano != second.Amount.Nano {
		return fmt.Errorf("%w: adjustment %q legs disagree", ErrJournalInvalid, journal.ID)
	}
	return nil
}

// checkALegPassThroughSnapshot binds one admitted journal to its exact
// canonical operation snapshot: recomputed integrity, sequence lockstep
// with the journal, and full balance agreement.
func checkALegPassThroughSnapshot(scope ALegAuthorityScope, journal *JournalTransaction, snapshot *ALegPassThroughSnapshot) error {
	if snapshot.Currency != scope.Currency {
		return fmt.Errorf("%w: adjustment snapshot %q currency mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.Fingerprint == "" {
		return fmt.Errorf("%w: adjustment snapshot %q has no fingerprint", ErrJournalInvalid, snapshot.OperationKey)
	}
	want := alegMarkerIntegrity(snapshot.OperationKey, scope.AccountID,
		CostPassThroughAdjustmentOperationKind, snapshot.SourceKey, snapshot.Fingerprint,
		snapshot.Before, snapshot.After, snapshot.SequenceStart, snapshot.SequenceEnd)
	if snapshot.IntegrityFingerprint == "" || snapshot.IntegrityFingerprint != want {
		return fmt.Errorf("%w: adjustment snapshot %q integrity mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.SequenceStart != snapshot.SequenceEnd || snapshot.SequenceStart != journal.AccountSequence {
		return fmt.Errorf("%w: adjustment snapshot %q sequence mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if journal.BalanceBefore != snapshot.Before.BalanceNano || journal.BalanceAfter != snapshot.After.BalanceNano ||
		journal.SpendableBefore != snapshot.Before.SpendableNano || journal.SpendableAfter != snapshot.After.SpendableNano ||
		journal.SnapshotVersionBefore != snapshot.Before.Version || journal.SnapshotVersionAfter != snapshot.After.Version ||
		journal.Mode != snapshot.Mode || journal.Currency != snapshot.Currency {
		return fmt.Errorf("%w: adjustment snapshot %q disagrees with journal %q", ErrJournalInvalid, snapshot.OperationKey, journal.ID)
	}
	return nil
}

// signedALegPassThroughDelta nets one admitted journal to its signed
// customer delta: positive is an additional customer debit, negative a
// customer credit. Unrepresentable nets fail the query.
func signedALegPassThroughDelta(currency string, journal *JournalTransaction) (int64, error) {
	var debit, credit int64
	var err error
	for _, entry := range journal.Entries {
		if entry.LedgerAccount != "customer_financial_account" {
			continue
		}
		if entry.Side == JournalDebit {
			debit, err = AddReportAmount(debit, entry.Amount, currency)
		} else {
			credit, err = AddReportAmount(credit, entry.Amount, currency)
		}
		if err != nil {
			return 0, fmt.Errorf("%w: A-leg pass-through delta: %v", ErrMoneyOverflow, err)
		}
	}
	delta, err := checkedSub(debit, credit)
	if err != nil {
		return 0, fmt.Errorf("%w: A-leg pass-through delta: %v", ErrMoneyOverflow, err)
	}
	return delta, nil
}
