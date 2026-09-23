package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Trusted provider COGS authority (refinement Task 5.3, Cycle 2B).
//
// The rolling A-leg report shell owns SQL, pagination, presence/journal
// retention bounds, and snapshot assembly. All per-leg provider proof —
// head identity, posting/execution fence agreement, canonical-rooted
// correction chains, legacy adoption, zero/exclusion records, and pending
// precedence — lives here as one side-effect-free pure function over
// already-loaded facts. No SQL, no I/O. Unknown is never zero; only
// infrastructure failures (unparseable identity) and unrepresentable
// money fail the query. Provider verdicts never touch retail totals.
//
// A nonzero known COGS requires the exact current revision-authority
// head plus its matching immutable journal chain plus posting and
// execution fence agreement. A head alone, a free-form valuation
// identity alone, or a legacy aggregate without revision cutover never
// suffices.

// Cycle 2B provider-authority issue codes owned by the core evaluator.
const (
	ALegProviderIssuePending    = "provider_cost_pending"
	ALegProviderIssueUnresolved = "provider_cost_unresolved"
)

// Explicit known-zero bases. The basis names the exact lineage that
// proves nothing is payable; consumers must not treat a zero basis as
// a missing measurement.
const (
	ALegProviderZeroNeverStarted = "never_started_not_billable"
	ALegProviderZeroRejected     = "rejected_not_payable"
	ALegProviderZeroRecorded     = "provider_recorded_zero"
	ALegProviderZeroExcluded     = "provider_excluded_non_payable"
	// ALegProviderZeroAllChildrenZero is the deterministic aggregate
	// basis when every evaluated payable child is proven zero: no
	// single child basis is preferred over another.
	ALegProviderZeroAllChildrenZero = "all_children_zero"
)

// alegProviderCOGSKind is the canonical provider COGS operation kind,
// shared with the durable writers.
const alegProviderCOGSKind = "provider_call_cogs"

const (
	alegProviderCOGSLedger    = "inference_provider_cogs"
	alegProviderPayableLedger = "provider_payable_clearing"
)

// ALegProviderHead is one trusted durable provider-cost head fact for
// the leg under proof, already loaded inside the report snapshot
// transaction. SubjectKind/SubjectID carry the durable head columns;
// Subject carries the decoded subject JSON. The evaluator requires all
// three to agree.
type ALegProviderHead struct {
	StoreID               string
	AccountID             string
	CallID                string
	HeadKey               string
	SubjectKind           string
	SubjectID             string
	Subject               metering.SubjectRef
	EvidenceRevision      uint64
	InputSetHash          string
	ValuationID           string
	CurrentAmount         Money
	HeadVersion           uint64
	Fence                 uint64
	LastOperationKey      string
	OriginalTransactionID string
	LastTransactionID     string
}

// ALegProviderFence is one durable posting-fence fact: the per-head
// monetary gate. The execution fence below is the amount-free writer
// gate shared across the B-leg lineage.
type ALegProviderFence struct {
	StoreID               string
	LineageKey            string
	Authority             string
	HeadKey               string
	EvidenceRevision      uint64
	InputSetHash          string
	Fingerprint           string
	Amount                Money
	Fence                 uint64
	LastOperationKey      string
	OriginalTransactionID string
	LastTransactionID     string
}

// ALegProviderExecutionFence is one durable execution-fence fact for the
// B-leg lineage under proof. It carries the full persisted owner
// envelope; the evaluator compares every field against the current
// head, posting fence, and leg scope.
type ALegProviderExecutionFence struct {
	StoreID           string
	LineageKey        string
	Authority         string
	OwnerSubjectKind  string
	OwnerHeadKey      string
	OwnerRevision     uint64
	OwnerInputSetHash string
	OwnerFingerprint  string
	Fence             uint64
	LastOperationKey  string
	LastTransactionID string
}

// ALegProviderSnapshot is one canonical provider operation snapshot
// fact, already loaded inside the report snapshot transaction. Account
// and operation kind ride the fact so the evaluator binds snapshots to
// the queried scope instead of trusting SQL scoping alone.
type ALegProviderSnapshot struct {
	AccountID            string
	OperationKind        string
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

// ALegProviderLegFacts bundles every provider fact the shell loads for
// one B-leg: outcome, pending signals, candidate heads, fences,
// snapshots, and journals. The evaluator filters each class to this
// leg; facts for other legs are ignored.
type ALegProviderLegFacts struct {
	BLegID          string
	Outcome         LegOutcome
	WorkPending     bool
	RevisionPending bool
	Heads           []ALegProviderHead
	PostingFences   []ALegProviderFence
	ExecutionFences []ALegProviderExecutionFence
	Snapshots       []ALegProviderSnapshot
	Journals        []JournalTransaction
}

// ALegProviderLegVerdict is the per-leg provider authority verdict for
// one snapshot. Cost with its operation lineage is set only when Status
// is known; ZeroBasis only when it is known_zero.
type ALegProviderLegVerdict struct {
	Status        ALegLegProviderStatus
	Cost          Money
	OperationKey  string
	TransactionID string
	ZeroBasis     string
	Children      []ALegProviderChild
}

// EvaluateALegProviderLeg proves one B-leg's provider authority inside
// the snapshot. It returns the verdict plus any issues; only
// infrastructure failures and unrepresentable money fail the query.
func EvaluateALegProviderLeg(scope ALegAuthorityScope, facts ALegProviderLegFacts) (ALegProviderLegVerdict, []ReconciliationIssue, error) {
	issue := func(code string, sequence uint64) ReconciliationIssue {
		return ReconciliationIssue{Code: code, Sequence: sequence, Detail: scope.CallID}
	}
	unknown := func(code string, sequence uint64) (ALegProviderLegVerdict, []ReconciliationIssue, error) {
		return ALegProviderLegVerdict{Status: ALegProviderUnknown}, []ReconciliationIssue{issue(code, sequence)}, nil
	}
	pending := func() (ALegProviderLegVerdict, []ReconciliationIssue, error) {
		return ALegProviderLegVerdict{Status: ALegProviderPending}, []ReconciliationIssue{issue(ALegProviderIssuePending, 0)}, nil
	}
	if facts.BLegID == "" {
		return ALegProviderLegVerdict{}, nil, fmt.Errorf("billing: A-leg provider leg identity is required")
	}
	parsedCallID, err := ParseBillingCallID(scope.CallID)
	if err != nil {
		return ALegProviderLegVerdict{}, nil, fmt.Errorf("billing: A-leg provider call identity: %w", err)
	}
	legKey, err := CallLegUsageKey(parsedCallID, facts.BLegID)
	if err != nil {
		return ALegProviderLegVerdict{}, nil, fmt.Errorf("billing: A-leg provider leg identity: %w", err)
	}
	var heads []*ALegProviderHead
	for i := range facts.Heads {
		head := &facts.Heads[i]
		if head.Subject.BLegID != facts.BLegID {
			continue
		}
		heads = append(heads, head)
	}
	var aggregates, children []*ALegProviderHead
	for _, head := range heads {
		switch head.Subject.Kind {
		case metering.SubjectBLeg:
			aggregates = append(aggregates, head)
		case metering.SubjectProviderCharge:
			children = append(children, head)
		default:
			return unknown(ALegProviderIssueUnresolved, 0)
		}
	}
	// Base B-leg subjects and provider-charge children are mutually
	// exclusive owner modes: the trusted writer fences one execution
	// owner at a time, so a mixed set can never be attributed. More
	// than one aggregate is likewise ambiguous.
	if len(aggregates) > 0 && len(children) > 0 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if len(aggregates) > 1 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	// Pending work or an in-flight revision always takes precedence over
	// zero and over an older head: outstanding evidence means the
	// current selection is not yet provable.
	if facts.WorkPending || facts.RevisionPending {
		return pending()
	}
	if len(aggregates) == 1 {
		return evaluateALegProviderAggregate(scope, legKey, facts, aggregates[0], unknown)
	}
	if len(children) > 0 {
		return evaluateALegProviderChildren(scope, legKey, facts, children, unknown)
	}
	journals := filterALegProviderJournals(scope, facts, facts.Journals)
	if len(journals) > 0 {
		// Without a head only writer-adopted legacy aggregates are
		// recognizable lineage, and even those stay pending until a
		// revision cutover owns them. Anything else is stray.
		for _, journal := range journals {
			if !isALegLegacyProviderJournal(scope, legKey, journal) {
				return unknown(ALegProviderIssueUnresolved, journal.AccountSequence)
			}
		}
		for _, journal := range journals {
			if err := proveALegProviderJournal(scope, facts.BLegID, journal, facts.Snapshots); err != nil {
				if _, ok := err.(alegProviderPendingEvidence); ok {
					return pending()
				}
				return unknown(ALegProviderIssueUnresolved, journal.AccountSequence)
			}
		}
		return pending()
	}
	var fences []ALegProviderFence
	for i := range facts.PostingFences {
		fence := &facts.PostingFences[i]
		if fence.LineageKey != legKey {
			continue
		}
		fences = append(fences, *fence)
	}
	if len(fences) > 1 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if len(fences) == 1 {
		fence := fences[0]
		// A revision-authority fence without a head is a recorded
		// non-payable exclusion. A legacy fence without lineage
		// evidence is pre-cutover state. Anything else is malformed.
		if fence.Authority == "revision" {
			if err := checkALegProviderExclusion(scope, legKey, fence, facts.ExecutionFences); err != nil {
				return unknown(ALegProviderIssueUnresolved, 0)
			}
			return ALegProviderLegVerdict{Status: ALegProviderKnownZero, ZeroBasis: ALegProviderZeroExcluded}, nil, nil
		}
		if fence.Authority == "legacy" {
			return pending()
		}
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if facts.Outcome == LegOutcomeNeverStarted {
		return ALegProviderLegVerdict{Status: ALegProviderKnownZero, ZeroBasis: ALegProviderZeroNeverStarted}, nil, nil
	}
	if facts.Outcome == LegOutcomeRejected {
		return ALegProviderLegVerdict{Status: ALegProviderKnownZero, ZeroBasis: ALegProviderZeroRejected}, nil, nil
	}
	return pending()
}

// alegProviderMaxChildren bounds evaluated provider-charge children per
// leg; beyond it the leg resolves unknown rather than a partial sum.
// Tests override it for small-scope proofs (never t.Parallel while
// overridden).
var alegProviderMaxChildren = 8

// ALegProviderChild is one independently evaluated provider-charge
// child: exact charge lineage with its own status, amount, current
// operation, and revision identity. Only known children contribute to
// leg and scope totals; entries for other statuses never appear on a
// known leg (unknown legs carry no child entries at all).
type ALegProviderChild struct {
	ChargeID         string
	HeadKey          string
	Status           ALegLegProviderStatus
	ZeroBasis        string
	Amount           Money
	OperationKey     string
	TransactionID    string
	EvidenceRevision uint64
	ValuationID      string
	InputSetHash     string
}

// alegProviderPendingEvidence marks absent (as opposed to contradictory)
// evidence: the leg stays pending rather than unknown.
type alegProviderPendingEvidence struct{ err error }

func (e alegProviderPendingEvidence) Error() string { return e.err.Error() }

// evaluateALegProviderAggregate proves the single base B-leg head path:
// the shared execution gate must name exactly this head, then the
// head unit proves itself.
func evaluateALegProviderAggregate(scope ALegAuthorityScope, legKey string, facts ALegProviderLegFacts, head *ALegProviderHead,
	unknown func(string, uint64) (ALegProviderLegVerdict, []ReconciliationIssue, error),
) (ALegProviderLegVerdict, []ReconciliationIssue, error) {
	var executions []ALegProviderExecutionFence
	for i := range facts.ExecutionFences {
		execution := &facts.ExecutionFences[i]
		if execution.LineageKey != legKey {
			continue
		}
		executions = append(executions, *execution)
	}
	if len(executions) != 1 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	var fences []ALegProviderFence
	for i := range facts.PostingFences {
		fence := &facts.PostingFences[i]
		if fence.LineageKey != legKey {
			continue
		}
		fences = append(fences, *fence)
	}
	if len(fences) != 1 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if err := checkALegProviderExecutionGate(scope, legKey, executions[0], head.Subject, head, fences[0]); err != nil {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	return proveALegProviderHeadUnit(scope, legKey, head, filterALegProviderJournals(scope, facts, facts.Journals), facts, unknown)
}

// proveALegProviderHeadUnit proves one head (base aggregate or single
// provider-charge child) against its own posting fence, journal
// partition, and current snapshots: exact subject and head identity,
// fence agreement, canonical-rooted chain, terminal and
// current-operation anchoring. The shared execution gate is checked by
// the caller once per leg, never per unit.
func proveALegProviderHeadUnit(scope ALegAuthorityScope, legKey string, head *ALegProviderHead, journals []*JournalTransaction, facts ALegProviderLegFacts,
	unknown func(string, uint64) (ALegProviderLegVerdict, []ReconciliationIssue, error),
) (ALegProviderLegVerdict, []ReconciliationIssue, error) {
	currency := scope.Currency
	subject := head.Subject
	if err := subject.Validate(); err != nil {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if subject.Kind != metering.SubjectBLeg && subject.Kind != metering.SubjectProviderCharge {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if head.SubjectKind != string(subject.Kind) || head.SubjectID != alegProviderSubjectID(subject) {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if scope.StoreID == "" || subject.StoreID != scope.StoreID {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if subject.BillingCallID != scope.CallID && subject.CallID != scope.CallID {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if subject.AccountID == "" || subject.AccountID != scope.AccountID {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if subject.ALegID == "" || subject.ALegID != scope.ALegID {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if head.StoreID != scope.StoreID || head.AccountID != scope.AccountID || head.CallID != scope.CallID || head.HeadKey == "" {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if head.CurrentAmount.Currency != currency || head.CurrentAmount.Nano < 0 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if head.EvidenceRevision == 0 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if head.InputSetHash == "" {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if _, err := NewEconomicRevisionIdentity(EconomicQueueProvider, head.HeadKey, head.EvidenceRevision, head.InputSetHash); err != nil {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if head.ValuationID == "" {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if err := economics.ValidateSafeRef("provider cost valuation id", head.ValuationID); err != nil {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if head.HeadVersion == 0 || head.Fence == 0 || head.HeadVersion != head.Fence {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if head.LastOperationKey == "" {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	lineage := legKey
	if subject.Kind == metering.SubjectProviderCharge {
		if subject.ProviderChargeID == "" {
			return unknown(ALegProviderIssueUnresolved, 0)
		}
		lineage = alegProviderChildLineage(legKey, subject.ProviderChargeID)
	}
	var fences []ALegProviderFence
	for i := range facts.PostingFences {
		fence := &facts.PostingFences[i]
		if fence.LineageKey != lineage {
			continue
		}
		fences = append(fences, *fence)
	}
	if len(fences) != 1 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	fence := fences[0]
	if fence.StoreID != scope.StoreID {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if fence.Authority != "revision" {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if fence.HeadKey != head.HeadKey || fence.EvidenceRevision != head.EvidenceRevision ||
		fence.InputSetHash != head.InputSetHash || fence.Amount != head.CurrentAmount {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if fence.Fingerprint == "" || fence.Fence <= 0 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if fence.LastOperationKey != head.LastOperationKey || fence.LastTransactionID != head.LastTransactionID {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	chain, pendingEvidence, err := proveALegProviderChain(scope, facts.BLegID, legKey, head, journals, facts.Snapshots)
	if err != nil {
		return ALegProviderLegVerdict{}, nil, err
	}
	if pendingEvidence {
		return ALegProviderLegVerdict{Status: ALegProviderPending}, []ReconciliationIssue{{Code: ALegProviderIssuePending, Sequence: 0, Detail: scope.CallID}}, nil
	}
	if !chain.complete {
		// Without journals only a complete current zero-delta proof
		// can establish authority; anything else is unresolved.
		if len(journals) == 0 && head.CurrentAmount.Nano == 0 {
			proved, issues, err := proveALegProviderCurrentZero(scope, head, fence, facts.Snapshots)
			if err != nil {
				if _, contradiction := err.(alegProviderContradiction); contradiction {
					return unknown(ALegProviderIssueUnresolved, 0)
				}
				return ALegProviderLegVerdict{}, nil, err
			}
			if !proved {
				return ALegProviderLegVerdict{Status: ALegProviderPending}, issues, nil
			}
			return ALegProviderLegVerdict{
				Status: ALegProviderKnownZero, ZeroBasis: ALegProviderZeroRecorded,
				OperationKey: head.LastOperationKey,
			}, nil, nil
		}
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if chain.rootID != head.OriginalTransactionID || head.OriginalTransactionID == "" {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if chain.terminalID != head.LastTransactionID || head.LastTransactionID == "" {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if chain.terminalID != head.LastOperationKey {
		// The current operation moved without a monetary delta: its
		// immutable snapshot must prove it separately. The transaction
		// pointer above already binds the last monetary journal.
		proved, issues, err := proveALegProviderCurrentZero(scope, head, fence, facts.Snapshots)
		if err != nil {
			if _, contradiction := err.(alegProviderContradiction); contradiction {
				return unknown(ALegProviderIssueUnresolved, 0)
			}
			return ALegProviderLegVerdict{}, nil, err
		}
		if !proved {
			return ALegProviderLegVerdict{Status: ALegProviderPending}, issues, nil
		}
	}
	if head.CurrentAmount.Nano > 0 {
		return ALegProviderLegVerdict{
			Status: ALegProviderKnown, Cost: head.CurrentAmount,
			OperationKey: head.LastOperationKey, TransactionID: head.LastTransactionID,
		}, nil, nil
	}
	return ALegProviderLegVerdict{
		Status: ALegProviderKnownZero, ZeroBasis: ALegProviderZeroRecorded,
		OperationKey: head.LastOperationKey, TransactionID: head.LastTransactionID,
	}, nil, nil
}

// checkALegProviderExecutionGate proves the shared execution gate
// against one named owner head and its posting fence: exact scope,
// revision authority, subject kind, head key, evidence revision, input
// hash, input fingerprint, current operation, and transaction, plus a
// positive fence counter. The counter is writer-positive and
// CAS-monotonic per lineage, but it counts execution-gate events while
// head and posting fences count head-local advances from independent
// seeds (fresh inserts, cutover recoveries, and upgrades start each
// counter at one separately), so no cross-counter equality exists by
// design: positivity is the exact checkable relation.
func checkALegProviderExecutionGate(scope ALegAuthorityScope, legKey string, execution ALegProviderExecutionFence, subject metering.SubjectRef, head *ALegProviderHead, fence ALegProviderFence) error {
	if execution.StoreID != scope.StoreID || execution.LineageKey != legKey {
		return fmt.Errorf("%w: provider execution gate scope mismatch", ErrJournalInvalid)
	}
	if execution.Authority != "revision" {
		return fmt.Errorf("%w: provider execution gate authority mismatch", ErrJournalInvalid)
	}
	if execution.OwnerSubjectKind != string(subject.Kind) || execution.OwnerHeadKey != head.HeadKey {
		return fmt.Errorf("%w: provider execution gate owner mismatch", ErrJournalInvalid)
	}
	if execution.OwnerRevision != head.EvidenceRevision || execution.OwnerInputSetHash != head.InputSetHash ||
		execution.OwnerFingerprint != fence.Fingerprint {
		return fmt.Errorf("%w: provider execution gate owner is stale", ErrJournalInvalid)
	}
	if execution.Fence == 0 {
		return fmt.Errorf("%w: provider execution gate fence is invalid", ErrJournalInvalid)
	}
	if execution.LastOperationKey != head.LastOperationKey || execution.LastTransactionID != head.LastTransactionID {
		return fmt.Errorf("%w: provider execution gate lineage mismatch", ErrJournalInvalid)
	}
	return nil
}

// evaluateALegProviderChildren proves every provider-charge child of
// one B-leg independently against its own posting fence, head, chain,
// and current snapshot, then aggregates atomically: the shared
// execution gate must exactly name one evaluated child, every child
// must resolve known for the leg to resolve known, and the checked sum
// of child amounts is the leg cost. Any pending child keeps the leg
// pending and any unknown child keeps it unknown; no partial subtotal
// is ever exposed. Children evaluate in charge-ID order so input
// permutation never changes the verdict or lineage ordering.
func evaluateALegProviderChildren(scope ALegAuthorityScope, legKey string, facts ALegProviderLegFacts, children []*ALegProviderHead,
	unknown func(string, uint64) (ALegProviderLegVerdict, []ReconciliationIssue, error),
) (ALegProviderLegVerdict, []ReconciliationIssue, error) {
	byCharge := make(map[string][]*ALegProviderHead, len(children))
	for _, head := range children {
		chargeID := head.Subject.ProviderChargeID
		if chargeID == "" {
			return unknown(ALegProviderIssueUnresolved, 0)
		}
		byCharge[chargeID] = append(byCharge[chargeID], head)
	}
	charges := make([]string, 0, len(byCharge))
	for chargeID, group := range byCharge {
		if len(group) != 1 {
			return unknown(ALegProviderIssueUnresolved, 0)
		}
		charges = append(charges, chargeID)
	}
	sort.Strings(charges)
	if len(charges) > alegProviderMaxChildren {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	var executions []ALegProviderExecutionFence
	for i := range facts.ExecutionFences {
		execution := &facts.ExecutionFences[i]
		if execution.LineageKey != legKey {
			continue
		}
		executions = append(executions, *execution)
	}
	if len(executions) != 1 {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	execution := executions[0]
	seenHeadKeys := make(map[string]bool, len(charges))
	for _, chargeID := range charges {
		head := byCharge[chargeID][0]
		if seenHeadKeys[head.HeadKey] {
			return unknown(ALegProviderIssueUnresolved, 0)
		}
		seenHeadKeys[head.HeadKey] = true
	}
	var named *ALegProviderHead
	var namedFence *ALegProviderFence
	for _, chargeID := range charges {
		head := byCharge[chargeID][0]
		if head.HeadKey != execution.OwnerHeadKey {
			continue
		}
		if named != nil {
			return unknown(ALegProviderIssueUnresolved, 0)
		}
		named = head
		var candidates []ALegProviderFence
		for i := range facts.PostingFences {
			fence := &facts.PostingFences[i]
			if fence.LineageKey != alegProviderChildLineage(legKey, chargeID) {
				continue
			}
			candidates = append(candidates, *fence)
		}
		if len(candidates) != 1 {
			return unknown(ALegProviderIssueUnresolved, 0)
		}
		selected := candidates[0]
		namedFence = &selected
	}
	if named == nil || namedFence == nil {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if err := checkALegProviderExecutionGate(scope, legKey, execution, named.Subject, named, *namedFence); err != nil {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	// Journals partition exactly by head correction group: every leg
	// journal must belong to exactly one evaluated child. Legacy-group
	// journals have no adopting aggregate here, and foreign groups are
	// overlapping coverage, so any journal outside the evaluated
	// head keys fails the leg closed instead of netting partially.
	byGroup := make(map[string][]*JournalTransaction)
	for _, journal := range filterALegProviderJournals(scope, facts, facts.Journals) {
		group := journal.CorrectionGroupID
		if group == "" {
			return unknown(ALegProviderIssueUnresolved, journal.AccountSequence)
		}
		byGroup[group] = append(byGroup[group], journal)
	}
	for group := range byGroup {
		if !seenHeadKeys[group] {
			return unknown(ALegProviderIssueUnresolved, 0)
		}
	}
	var entries []ALegProviderChild
	var total int64
	complete := true
	pendingLeg := false
	allZero := true
	for _, chargeID := range charges {
		head := byCharge[chargeID][0]
		var unitJournals []*JournalTransaction
		unitJournals = append(unitJournals, byGroup[head.HeadKey]...)
		unit, _, err := proveALegProviderHeadUnit(scope, legKey, head, unitJournals, facts, unknown)
		if err != nil {
			return ALegProviderLegVerdict{}, nil, err
		}
		if unit.Status == ALegProviderPending {
			pendingLeg = true
			continue
		}
		if unit.Status == ALegProviderUnknown {
			complete = false
			continue
		}
		if unit.Status != ALegProviderKnownZero {
			allZero = false
		}
		entry := ALegProviderChild{
			ChargeID: chargeID, HeadKey: head.HeadKey,
			Status: unit.Status, ZeroBasis: unit.ZeroBasis, Amount: unit.Cost,
			OperationKey: unit.OperationKey, TransactionID: unit.TransactionID,
			EvidenceRevision: head.EvidenceRevision, ValuationID: head.ValuationID, InputSetHash: head.InputSetHash,
		}
		entries = append(entries, entry)
		if unit.Status == ALegProviderKnown {
			var err error
			total, err = checkedAdd(total, unit.Cost.Nano)
			if err != nil {
				return ALegProviderLegVerdict{}, nil, fmt.Errorf("%w: A-leg provider child net: %v", ErrMoneyOverflow, err)
			}
		}
	}
	if !complete {
		return unknown(ALegProviderIssueUnresolved, 0)
	}
	if pendingLeg {
		return ALegProviderLegVerdict{Status: ALegProviderPending}, []ReconciliationIssue{{Code: ALegProviderIssuePending, Sequence: 0, Detail: scope.CallID}}, nil
	}
	if allZero {
		return ALegProviderLegVerdict{
			Status: ALegProviderKnownZero, ZeroBasis: ALegProviderZeroAllChildrenZero,
			Cost:         Money{Nano: 0, Currency: scope.Currency},
			OperationKey: execution.LastOperationKey, TransactionID: execution.LastTransactionID,
			Children: entries,
		}, nil, nil
	}
	return ALegProviderLegVerdict{
		Status: ALegProviderKnown, Cost: Money{Nano: total, Currency: scope.Currency},
		OperationKey: execution.LastOperationKey, TransactionID: execution.LastTransactionID,
		Children: entries,
	}, nil, nil
}

// persists beside the decoded subject JSON (see subjectIDForEconomics):
// B-leg subjects identify by B-leg, provider-charge subjects by charge.
// Any other kind yields no identity and can never agree.
func alegProviderSubjectID(subject metering.SubjectRef) string {
	switch subject.Kind {
	case metering.SubjectBLeg:
		return subject.BLegID
	case metering.SubjectProviderCharge:
		return subject.ProviderChargeID
	default:
		return ""
	}
}

// alegProviderChildLineage mirrors the durable posting lineage the
// writer derives for provider-charge children sharing one B-leg
// execution: the leg lineage suffixed by the charge identity.
func alegProviderChildLineage(legKey, chargeID string) string {
	return legKey + ":provider-charge:" + chargeID
}

// alegProviderContradiction marks contradictory (as opposed to absent)
// zero-delta evidence: the leg stays unknown rather than pending.
type alegProviderContradiction struct{ err error }

func (e alegProviderContradiction) Error() string { return e.err.Error() }

// alegProviderRevisionSource derives the expected canonical revision
// source for a head revision from normalized writer inputs. It mirrors
// the durable preimage of ProviderCostRevisionSourceKey
// (account/call/head/revision/input-hash digest) so the report can
// bind a snapshot to the current revision without the full rating
// input; a dedicated test proves the derivation equal against the
// writer function. All inputs must already be normalized: the caller
// validates revision, hash, and identity before invoking it.
func alegProviderRevisionSource(accountID string, callID BillingCallID, headKey string, revision uint64, inputSetHash string) (string, error) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(headKey) == "" || strings.TrimSpace(inputSetHash) == "" || revision == 0 {
		return "", fmt.Errorf("%w: incomplete revision source identity", ErrProviderCostRevisionInvalid)
	}
	if err := callID.Validate(); err != nil {
		return "", fmt.Errorf("%w: revision source call: %v", ErrProviderCostRevisionInvalid, err)
	}
	payload := strings.TrimSpace(accountID) + "\x00" + callID.String() + "\x00" + strings.TrimSpace(headKey) + "\x00" + fmt.Sprint(revision) + "\x00" + strings.TrimSpace(inputSetHash)
	digest := sha256.Sum256([]byte(payload))
	return "provider-cost-revision:v1:" + hex.EncodeToString(digest[:]), nil
}

// proveALegProviderCurrentZero proves a journal-less current operation:
// the immutable operation snapshot keyed by the head's current
// operation binds to the posting fence fingerprint and to the derived
// canonical revision source, with recomputed integrity, zero sequence,
// and identical balance snapshots. It returns proved=false with issues
// when the snapshot is absent, and a contradiction error when
// present-but-malformed. The snapshot never fabricates a monetary
// transaction: LastTransactionID keeps pointing at the last admitted
// journal (possibly empty for a first zero), which the caller binds
// separately.
func proveALegProviderCurrentZero(scope ALegAuthorityScope, head *ALegProviderHead, fence ALegProviderFence, snapshots []ALegProviderSnapshot) (bool, []ReconciliationIssue, error) {
	issue := ReconciliationIssue{Code: ALegProviderIssuePending, Sequence: 0, Detail: scope.CallID}
	contradiction := func(format string, args ...any) (bool, []ReconciliationIssue, error) {
		return false, nil, alegProviderContradiction{err: fmt.Errorf(format, args...)}
	}
	callID, err := ParseBillingCallID(scope.CallID)
	if err != nil {
		return false, nil, fmt.Errorf("billing: A-leg provider call identity: %w", err)
	}
	expectedSource, err := alegProviderRevisionSource(scope.AccountID, callID, head.HeadKey, head.EvidenceRevision, head.InputSetHash)
	if err != nil {
		return contradiction("%w: provider current revision source: %v", ErrJournalInvalid, err)
	}
	var snapshot *ALegProviderSnapshot
	for i := range snapshots {
		candidate := &snapshots[i]
		if candidate.OperationKey != head.LastOperationKey {
			continue
		}
		if snapshot != nil {
			return contradiction("%w: duplicate provider current snapshot for %q", ErrJournalInvalid, head.LastOperationKey)
		}
		snapshot = candidate
	}
	if snapshot == nil {
		return false, []ReconciliationIssue{issue}, nil
	}
	if snapshot.AccountID != scope.AccountID || snapshot.OperationKind != alegProviderCOGSKind {
		return contradiction("%w: provider current snapshot %q scope mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.SourceKey == "" || snapshot.SourceKey != expectedSource {
		return contradiction("%w: provider current snapshot %q source mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.Currency != scope.Currency || snapshot.Fingerprint == "" || snapshot.Fingerprint != fence.Fingerprint {
		return contradiction("%w: provider current snapshot %q identity mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	want := alegMarkerIntegrity(snapshot.OperationKey, scope.AccountID,
		alegProviderCOGSKind, snapshot.SourceKey, snapshot.Fingerprint,
		snapshot.Before, snapshot.After, snapshot.SequenceStart, snapshot.SequenceEnd)
	if snapshot.IntegrityFingerprint == "" || snapshot.IntegrityFingerprint != want {
		return contradiction("%w: provider current snapshot %q integrity mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.SequenceStart != 0 || snapshot.SequenceEnd != 0 {
		return contradiction("%w: provider current snapshot %q carries journal sequence", ErrJournalInvalid, snapshot.OperationKey)
	}
	if !reflect.DeepEqual(snapshot.Before, snapshot.After) {
		return contradiction("%w: provider current snapshot %q moves balances", ErrJournalInvalid, snapshot.OperationKey)
	}
	return true, nil, nil
}

// filterALegProviderJournals selects this leg's provider journals. Other
// calls, other legs, and non-provider planes are never this leg's
// lineage.
func filterALegProviderJournals(scope ALegAuthorityScope, facts ALegProviderLegFacts, journals []JournalTransaction) []*JournalTransaction {
	var out []*JournalTransaction
	for i := range journals {
		journal := &journals[i]
		if journal.OperationKind != alegProviderCOGSKind || journal.TurnID != scope.CallID || journal.BLegID != facts.BLegID {
			continue
		}
		out = append(out, journal)
	}
	return out
}

// isALegLegacyProviderJournal reports whether a journal carries the exact
// legacy aggregate writer shape: no correction links and the legacy
// lineage operation identity.
func isALegLegacyProviderJournal(scope ALegAuthorityScope, legKey string, journal *JournalTransaction) bool {
	legacySource, err := ProviderCostSourceKey(legKey)
	if err != nil {
		return false
	}
	legacyOp := ScopedOperationKey(alegProviderCOGSKind, scope.AccountID, legacySource)
	return journal.ReversalOf == "" && journal.CorrectsTransactionID == "" &&
		journal.CorrectionGroupID == legKey && journal.ID == legacyOp && journal.SourceKey == legacyOp
}

// proveALegProviderJournal validates one legacy journal fully: sealed
// identity, scope, writer shape, and snapshot binding. Absent snapshots
// report pending evidence; contradictions fail.
func proveALegProviderJournal(scope ALegAuthorityScope, bLegID string, journal *JournalTransaction, snapshots []ALegProviderSnapshot) error {
	if err := checkALegProviderJournal(scope, bLegID, journal); err != nil {
		return err
	}
	if err := checkALegProviderSnapshot(scope, journal, snapshots); err != nil {
		return err
	}
	return nil
}

// checkALegProviderJournal validates sealed identity, scope, and the
// exact writer pair shape of one provider journal.
func checkALegProviderJournal(scope ALegAuthorityScope, bLegID string, journal *JournalTransaction) error {
	if journal.Book != JournalBookFinancial {
		return fmt.Errorf("%w: provider journal %q book %q", ErrJournalInvalid, journal.ID, journal.Book)
	}
	if journal.Currency != scope.Currency {
		return fmt.Errorf("%w: provider journal %q currency mismatch", ErrJournalInvalid, journal.ID)
	}
	if err := journal.Validate(); err != nil {
		return err
	}
	fingerprint, err := journal.CanonicalFingerprint()
	if err != nil {
		return err
	}
	if journal.SemanticFingerprint != fingerprint {
		return fmt.Errorf("%w: provider journal %q stored fingerprint does not match sealed content", ErrJournalFingerprint, journal.ID)
	}
	if journal.AccountID != scope.AccountID || journal.TurnID != scope.CallID ||
		journal.ALegID != scope.ALegID || journal.BLegID != bLegID {
		return fmt.Errorf("%w: provider journal %q is outside report scope", ErrJournalInvalid, journal.ID)
	}
	if len(journal.Entries) != 2 {
		return fmt.Errorf("%w: provider journal %q must carry exactly the writer pair", ErrJournalInvalid, journal.ID)
	}
	first, second := journal.Entries[0], journal.Entries[1]
	cogs := func(entry JournalEntry, side JournalSide) bool {
		return entry.LedgerAccount == alegProviderCOGSLedger && entry.Side == side
	}
	clearing := func(entry JournalEntry, side JournalSide) bool {
		return entry.LedgerAccount == alegProviderPayableLedger && entry.Side == side
	}
	forward := cogs(first, JournalDebit) && clearing(second, JournalCredit) ||
		cogs(second, JournalDebit) && clearing(first, JournalCredit)
	mirror := cogs(first, JournalCredit) && clearing(second, JournalDebit) ||
		cogs(second, JournalCredit) && clearing(first, JournalDebit)
	if !forward && !mirror {
		return fmt.Errorf("%w: provider journal %q carries a non-canonical ledger shape", ErrJournalInvalid, journal.ID)
	}
	if first.Amount.Nano != second.Amount.Nano {
		return fmt.Errorf("%w: provider journal %q legs disagree", ErrJournalInvalid, journal.ID)
	}
	return nil
}

// checkALegProviderSnapshot binds one journal to its exact canonical
// operation snapshot: recomputed integrity, sequence lockstep with the
// journal, identical balance snapshots (provider posts never move
// balances), and full balance agreement.
func checkALegProviderSnapshot(scope ALegAuthorityScope, journal *JournalTransaction, snapshots []ALegProviderSnapshot) error {
	var snapshot *ALegProviderSnapshot
	for i := range snapshots {
		candidate := &snapshots[i]
		if candidate.OperationKey != journal.ID {
			continue
		}
		if snapshot != nil {
			return fmt.Errorf("%w: duplicate provider snapshot for %q", ErrJournalInvalid, journal.ID)
		}
		snapshot = candidate
	}
	if snapshot == nil {
		return alegProviderPendingEvidence{err: fmt.Errorf("provider snapshot for %q is absent", journal.ID)}
	}
	if snapshot.AccountID != scope.AccountID || snapshot.OperationKind != alegProviderCOGSKind {
		return fmt.Errorf("%w: provider snapshot %q scope mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.SourceKey == "" {
		return fmt.Errorf("%w: provider snapshot %q has no source", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.Currency != scope.Currency {
		return fmt.Errorf("%w: provider snapshot %q currency mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.Fingerprint == "" {
		return fmt.Errorf("%w: provider snapshot %q has no fingerprint", ErrJournalInvalid, snapshot.OperationKey)
	}
	want := alegMarkerIntegrity(snapshot.OperationKey, scope.AccountID,
		alegProviderCOGSKind, snapshot.SourceKey, snapshot.Fingerprint,
		snapshot.Before, snapshot.After, snapshot.SequenceStart, snapshot.SequenceEnd)
	if snapshot.IntegrityFingerprint == "" || snapshot.IntegrityFingerprint != want {
		return fmt.Errorf("%w: provider snapshot %q integrity mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if snapshot.SequenceStart != snapshot.SequenceEnd || snapshot.SequenceStart != journal.AccountSequence {
		return fmt.Errorf("%w: provider snapshot %q sequence mismatch", ErrJournalInvalid, snapshot.OperationKey)
	}
	if !reflect.DeepEqual(snapshot.Before, snapshot.After) {
		return fmt.Errorf("%w: provider snapshot %q moves balances", ErrJournalInvalid, snapshot.OperationKey)
	}
	if journal.BalanceBefore != snapshot.Before.BalanceNano || journal.BalanceAfter != snapshot.After.BalanceNano ||
		journal.SpendableBefore != snapshot.Before.SpendableNano || journal.SpendableAfter != snapshot.After.SpendableNano ||
		journal.SnapshotVersionBefore != snapshot.Before.Version || journal.SnapshotVersionAfter != snapshot.After.Version ||
		journal.Mode != snapshot.Mode || journal.Currency != snapshot.Currency {
		return fmt.Errorf("%w: provider snapshot %q disagrees with journal %q", ErrJournalInvalid, snapshot.OperationKey, journal.ID)
	}
	return nil
}

type alegProviderChain struct {
	complete   bool
	rootID     string
	terminalID string
}

// proveALegProviderChain walks the canonical-rooted immutable signed
// chain for one head: exactly one root (revision or adopted legacy),
// linear latest-to-prior links, every journal consumed exactly once,
// and checked telescoping from zero to the head amount. Structure is
// classified before content so stray lineage fails closed even when
// snapshots are absent; a journal with an absent snapshot reports
// pending evidence; unrepresentable nets fail the query.
//
// Chained deltas carry the root's correction group: the writer clears
// the group on the delta and the generic correction gate rebinds it to
// the target's group before commit. The evaluator therefore requires
// every chained journal to name the root's group, never an empty one.
func proveALegProviderChain(scope ALegAuthorityScope, bLegID, legKey string, head *ALegProviderHead, journals []*JournalTransaction, snapshots []ALegProviderSnapshot) (alegProviderChain, bool, error) {
	currency := scope.Currency
	legacySource, err := ProviderCostSourceKey(legKey)
	if err != nil {
		return alegProviderChain{}, false, fmt.Errorf("billing: A-leg provider lineage: %w", err)
	}
	legacyOp := ScopedOperationKey(alegProviderCOGSKind, scope.AccountID, legacySource)
	ordered := append([]*JournalTransaction(nil), journals...)
	sort.Slice(ordered, func(a, b int) bool {
		if ordered[a].AccountSequence != ordered[b].AccountSequence {
			return ordered[a].AccountSequence < ordered[b].AccountSequence
		}
		return ordered[a].ID < ordered[b].ID
	})
	// Structural classification first: roots, links, and strays are
	// link-deterministic, so outcomes never depend on input order.
	var root *JournalTransaction
	for _, journal := range ordered {
		linked := journal.ReversalOf != "" || journal.CorrectsTransactionID != ""
		if !linked {
			isRevisionRoot := journal.CorrectionGroupID == head.HeadKey
			isLegacyRoot := journal.CorrectionGroupID == legKey && journal.ID == legacyOp && journal.SourceKey == legacyOp
			if !isRevisionRoot && !isLegacyRoot {
				return alegProviderChain{}, false, nil
			}
			if root != nil {
				return alegProviderChain{}, false, nil
			}
			root = journal
		}
	}
	if root == nil {
		return alegProviderChain{}, false, nil
	}
	consumed := map[string]bool{root.ID: true}
	current := root
	for {
		var next *JournalTransaction
		for _, journal := range ordered {
			if consumed[journal.ID] {
				continue
			}
			if journal.ReversalOf != current.ID {
				continue
			}
			if next != nil {
				return alegProviderChain{}, false, nil
			}
			next = journal
		}
		if next == nil {
			break
		}
		if next.CorrectsTransactionID != current.ID || next.CorrectionGroupID != root.CorrectionGroupID {
			return alegProviderChain{}, false, nil
		}
		consumed[next.ID] = true
		current = next
	}
	if len(consumed) != len(ordered) {
		return alegProviderChain{}, false, nil
	}
	// Content proof over the admitted chain in deterministic order.
	deltas := make(map[string]int64, len(ordered))
	for _, journal := range ordered {
		if err := checkALegProviderJournal(scope, bLegID, journal); err != nil {
			return alegProviderChain{}, false, nil
		}
		if err := checkALegProviderSnapshot(scope, journal, snapshots); err != nil {
			if _, ok := err.(alegProviderPendingEvidence); ok {
				return alegProviderChain{}, true, nil
			}
			return alegProviderChain{}, false, nil
		}
		delta, err := signedALegProviderDelta(currency, journal)
		if err != nil {
			return alegProviderChain{}, false, err
		}
		deltas[journal.ID] = delta
	}
	running := int64(0)
	current = root
	for {
		var err error
		running, err = checkedAdd(running, deltas[current.ID])
		if err != nil {
			return alegProviderChain{}, false, fmt.Errorf("%w: A-leg provider chain net: %v", ErrMoneyOverflow, err)
		}
		var next *JournalTransaction
		for _, journal := range ordered {
			if !consumed[journal.ID] || journal.ID == current.ID {
				continue
			}
			if journal.ReversalOf == current.ID {
				next = journal
				break
			}
		}
		if next == nil {
			break
		}
		current = next
	}
	if running != head.CurrentAmount.Nano {
		return alegProviderChain{}, false, nil
	}
	return alegProviderChain{complete: true, rootID: root.ID, terminalID: current.ID}, false, nil
}

// signedALegProviderDelta nets one admitted journal to its signed
// operator delta: positive accrues COGS, negative reverses it.
// Unrepresentable nets fail the query.
func signedALegProviderDelta(currency string, journal *JournalTransaction) (int64, error) {
	var debit, credit int64
	var err error
	for _, entry := range journal.Entries {
		if entry.LedgerAccount != alegProviderCOGSLedger {
			continue
		}
		if entry.Side == JournalDebit {
			debit, err = AddReportAmount(debit, entry.Amount, currency)
		} else {
			credit, err = AddReportAmount(credit, entry.Amount, currency)
		}
		if err != nil {
			return 0, fmt.Errorf("%w: A-leg provider delta: %v", ErrMoneyOverflow, err)
		}
	}
	delta, err := checkedSub(debit, credit)
	if err != nil {
		return 0, fmt.Errorf("%w: A-leg provider delta: %v", ErrMoneyOverflow, err)
	}
	return delta, nil
}

// checkALegProviderExclusion validates a recorded non-payable exclusion:
// a revision-authority fence with no head and a zero selected amount.
func checkALegProviderExclusion(scope ALegAuthorityScope, legKey string, fence ALegProviderFence, executions []ALegProviderExecutionFence) error {
	if fence.StoreID != scope.StoreID {
		return fmt.Errorf("%w: provider exclusion scope mismatch", ErrJournalInvalid)
	}
	if fence.HeadKey == "" || fence.EvidenceRevision == 0 || fence.InputSetHash == "" || fence.Fingerprint == "" {
		return fmt.Errorf("%w: provider exclusion identity is incomplete", ErrJournalInvalid)
	}
	if fence.LastOperationKey == "" {
		return fmt.Errorf("%w: provider exclusion has no operation", ErrJournalInvalid)
	}
	if fence.Amount.Nano != 0 || fence.Amount.Currency != scope.Currency {
		return fmt.Errorf("%w: provider exclusion amount mismatch", ErrJournalInvalid)
	}
	if fence.Fence <= 0 {
		return fmt.Errorf("%w: provider exclusion fence is invalid", ErrJournalInvalid)
	}
	// The exclusion fence owns no head, but the execution gate still
	// names its current owner: the gate must agree on lineage, head,
	// revision, hash, fingerprint, operation, and transaction.
	var executionsForLeg []ALegProviderExecutionFence
	for i := range executions {
		execution := &executions[i]
		if execution.LineageKey != legKey {
			continue
		}
		executionsForLeg = append(executionsForLeg, *execution)
	}
	if len(executionsForLeg) != 1 {
		return fmt.Errorf("%w: provider exclusion execution gate is missing", ErrJournalInvalid)
	}
	execution := executionsForLeg[0]
	if execution.StoreID != scope.StoreID {
		return fmt.Errorf("%w: provider exclusion execution scope mismatch", ErrJournalInvalid)
	}
	if execution.Authority != "revision" {
		return fmt.Errorf("%w: provider exclusion execution authority mismatch", ErrJournalInvalid)
	}
	if execution.OwnerSubjectKind != string(metering.SubjectBLeg) || execution.OwnerHeadKey != fence.HeadKey {
		return fmt.Errorf("%w: provider exclusion execution owner mismatch", ErrJournalInvalid)
	}
	if execution.OwnerRevision != fence.EvidenceRevision || execution.OwnerInputSetHash != fence.InputSetHash ||
		execution.OwnerFingerprint != fence.Fingerprint {
		return fmt.Errorf("%w: provider exclusion execution owner is stale", ErrJournalInvalid)
	}
	if execution.Fence == 0 {
		return fmt.Errorf("%w: provider exclusion execution fence is invalid", ErrJournalInvalid)
	}
	if execution.LastOperationKey != fence.LastOperationKey || execution.LastTransactionID != fence.LastTransactionID {
		return fmt.Errorf("%w: provider exclusion execution lineage mismatch", ErrJournalInvalid)
	}
	return nil
}
