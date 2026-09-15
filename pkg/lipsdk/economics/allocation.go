package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// AllocationVersionV1 is the first immutable explicit allocation envelope.
const AllocationVersionV1 uint64 = 1

const (
	MaxAllocationTargets = 4096
	MaxAllocationRefs    = 1024
)

var (
	ErrInvalidAllocation              = errors.New("economics: invalid allocation")
	ErrAllocationNotConserved         = errors.New("economics: allocation weights are not conserved")
	ErrAllocationTargetConflict       = errors.New("economics: allocation target conflict")
	ErrAllocationScopeMismatch        = errors.New("economics: allocation scope mismatch")
	ErrAllocationInvalidSource        = errors.New("economics: invalid allocation source")
	ErrAllocationSignMismatch         = errors.New("economics: allocation sign does not match operation")
	ErrAllocationRevisionRequired     = errors.New("economics: allocation revision required")
	ErrAllocationResidualUnassigned   = errors.New("economics: allocation rounding residual is unassigned")
	ErrAllocationSupersessionConflict = errors.New("economics: allocation supersession conflict")
	ErrAllocationSupersessionCycle    = errors.New("economics: allocation supersession cycle")
	ErrAllocationSupersessionFork     = errors.New("economics: allocation supersession fork")
	// HeadConflict is an alias of Fork so callers can classify either wording
	// without creating a second incompatible error taxonomy.
	ErrAllocationSupersessionHeadConflict = ErrAllocationSupersessionFork
)

// AllocationOperation describes the typed sign semantics of an allocation.
// Corrections are additive immutable records and refer to the superseded
// allocation; they never rewrite an earlier record.
type AllocationOperation string

const (
	AllocationOperationAllocate    AllocationOperation = "allocate"
	AllocationOperationRefund      AllocationOperation = "refund"
	AllocationOperationCorrection  AllocationOperation = "correction"
	AllocationOperationReplacement AllocationOperation = "replacement"
	AllocationOperationZero        AllocationOperation = "zero"
	AllocationAllocate                                 = AllocationOperationAllocate
	AllocationRefund                                   = AllocationOperationRefund
	AllocationCorrection                               = AllocationOperationCorrection
	AllocationReplacement                              = AllocationOperationReplacement
	AllocationOperationReplace                         = AllocationOperationReplacement
	AllocationZero                                     = AllocationOperationZero
)

func (o AllocationOperation) IsKnown() bool {
	switch o {
	case AllocationOperationAllocate, AllocationOperationRefund, AllocationOperationCorrection, AllocationOperationReplacement, AllocationOperationZero:
		return true
	default:
		return false
	}
}

// AllocationResidualPolicy makes the treatment of a rounded residual an
// explicit part of replay identity. ToLastTarget means the final economic
// target (an explicit unallocated remainder is not preferred); ToUnallocated
// requires a target marked Unallocated.
type AllocationResidualPolicy string

const (
	AllocationResidualReject        AllocationResidualPolicy = "reject"
	AllocationResidualToLastTarget  AllocationResidualPolicy = "to_last_target"
	AllocationResidualToUnallocated AllocationResidualPolicy = "to_unallocated"
	AllocationResidualToLast                                 = AllocationResidualToLastTarget
	AllocationResidualToRemainder                            = AllocationResidualToUnallocated
)

func (p AllocationResidualPolicy) IsKnown() bool {
	switch p {
	case AllocationResidualReject, AllocationResidualToLastTarget, AllocationResidualToUnallocated:
		return true
	default:
		return false
	}
}

// AllocationPolicyRef identifies the frozen policy that produced the weights.
// Hash is normally a lower-case SHA-256 content hash of the policy definition.
type AllocationPolicyRef struct {
	Method  string `json:"method"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

func (p AllocationPolicyRef) normalize() (AllocationPolicyRef, error) {
	out := p
	for name, value := range map[string]string{"allocation policy method": out.Method, "allocation policy version": out.Version, "allocation policy hash": out.Hash} {
		if err := ValidateSafeRef(name, value); err != nil {
			return AllocationPolicyRef{}, fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
		}
	}
	out.Hash = strings.ToLower(out.Hash)
	if decoded, err := hex.DecodeString(out.Hash); err != nil || len(decoded) != sha256.Size {
		return AllocationPolicyRef{}, fmt.Errorf("%w: allocation policy hash must be SHA-256 hex", ErrInvalidAllocation)
	}
	return out, nil
}

// Validate checks the policy reference without changing it.
func (p AllocationPolicyRef) Validate() error {
	_, err := p.normalize()
	return err
}

// AllocationFraction is an exact non-negative rational. Numerator and
// Denominator are strings to avoid floating-point loss in durable identities.
type AllocationFraction struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}

func (f AllocationFraction) Normalize() (AllocationFraction, error) {
	if strings.TrimSpace(f.Numerator) != f.Numerator || strings.TrimSpace(f.Denominator) != f.Denominator || f.Numerator == "" || f.Denominator == "" {
		return AllocationFraction{}, fmt.Errorf("%w: fraction numerator and denominator are required", ErrInvalidAllocation)
	}
	if len(f.Numerator)+len(f.Denominator) > MaxValuationRationalDigits {
		return AllocationFraction{}, fmt.Errorf("%w: fraction exceeds %d digits", ErrInvalidAllocation, MaxValuationRationalDigits)
	}
	if strings.HasPrefix(f.Numerator, "+") || strings.HasPrefix(f.Denominator, "+") {
		return AllocationFraction{}, fmt.Errorf("%w: fraction cannot carry a leading plus", ErrInvalidAllocation)
	}
	numerator, ok := new(big.Int).SetString(f.Numerator, 10)
	if !ok || numerator.Sign() < 0 {
		return AllocationFraction{}, fmt.Errorf("%w: fraction numerator must be a non-negative integer", ErrInvalidAllocation)
	}
	denominator, ok := new(big.Int).SetString(f.Denominator, 10)
	if !ok || denominator.Sign() <= 0 {
		return AllocationFraction{}, fmt.Errorf("%w: fraction denominator must be positive", ErrInvalidAllocation)
	}
	gcd := new(big.Int).GCD(nil, nil, numerator, denominator)
	numerator.Div(numerator, gcd)
	denominator.Div(denominator, gcd)
	return AllocationFraction{Numerator: numerator.String(), Denominator: denominator.String()}, nil
}

// Validate checks an exact fraction without changing it.
func (f AllocationFraction) Validate() error {
	_, err := f.Normalize()
	return err
}

func (f AllocationFraction) Rat() (*big.Rat, error) {
	normalized, err := f.Normalize()
	if err != nil {
		return nil, err
	}
	numerator, _ := new(big.Int).SetString(normalized.Numerator, 10)
	denominator, _ := new(big.Int).SetString(normalized.Denominator, 10)
	return new(big.Rat).SetFrac(numerator, denominator), nil
}

// AllocationRoundedAmount is the integer ledger projection of an exact
// amount. The exact SourceAmount and target Share remain authoritative.
type AllocationRoundedAmount struct {
	NanoUnits int64          `json:"nano_units"`
	Currency  string         `json:"currency"`
	Present   bool           `json:"present"`
	Policy    RoundingPolicy `json:"policy"`
}

func (a AllocationRoundedAmount) Validate() error {
	if !a.Present {
		if a.NanoUnits != 0 || a.Currency != "" || a.Policy != RoundingUnspecified {
			return fmt.Errorf("%w: absent rounded amount carries value", ErrInvalidAllocation)
		}
		return nil
	}
	if _, err := NormalizeCurrency(a.Currency); err != nil {
		return fmt.Errorf("%w: rounded currency: %v", ErrInvalidAllocation, err)
	}
	if !a.Policy.IsKnown() || a.Policy == RoundingUnspecified {
		return fmt.Errorf("%w: rounded amount policy required", ErrInvalidAllocation)
	}
	return nil
}

// AllocationRef points to one immutable allocation revision.
type AllocationRef struct {
	StoreID      string `json:"store_id"`
	AllocationID string `json:"allocation_id"`
	Version      uint64 `json:"version"`
	PayloadHash  string `json:"payload_hash"`
}

func (r AllocationRef) normalize() error {
	for name, value := range map[string]string{"allocation ref store_id": r.StoreID, "allocation ref id": r.AllocationID, "allocation ref payload_hash": r.PayloadHash} {
		if err := ValidateSafeRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
		}
	}
	if r.Version == 0 {
		return fmt.Errorf("%w: allocation ref version required", ErrInvalidAllocation)
	}
	if decoded, err := hex.DecodeString(r.PayloadHash); err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("%w: allocation ref payload hash must be SHA-256 hex", ErrInvalidAllocation)
	}
	return nil
}

// Validate checks an immutable allocation reference.
func (r AllocationRef) Validate() error { return r.normalize() }

// AllocationValuationRef retains the immutable valuation basis used as input
// without making the allocation a provider charge or a request debit.
type AllocationValuationRef struct {
	StoreID     string `json:"store_id"`
	ValuationID string `json:"valuation_id"`
	Version     uint64 `json:"version"`
	PayloadHash string `json:"payload_hash"`
}

func (r AllocationValuationRef) normalize() error {
	for name, value := range map[string]string{"allocation valuation ref store_id": r.StoreID, "allocation valuation ref id": r.ValuationID, "allocation valuation ref payload_hash": r.PayloadHash} {
		if err := ValidateSafeRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
		}
	}
	if r.Version == 0 {
		return fmt.Errorf("%w: allocation valuation ref version required", ErrInvalidAllocation)
	}
	if decoded, err := hex.DecodeString(r.PayloadHash); err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("%w: allocation valuation ref payload hash must be SHA-256 hex", ErrInvalidAllocation)
	}
	return nil
}

// Validate checks an immutable valuation reference.
func (r AllocationValuationRef) Validate() error { return r.normalize() }

// AllocationTarget is one immutable target and exact share. Unallocated is a
// first-class target with no subject, making a remainder auditable rather than
// silently losing source cost.
type AllocationTarget struct {
	TargetID             string                   `json:"target_id"`
	Target               metering.SubjectRef      `json:"target,omitzero"`
	Unallocated          bool                     `json:"unallocated,omitempty"`
	Informational        bool                     `json:"informational,omitempty"`
	AccountID            string                   `json:"account_id,omitempty"`
	PeriodID             string                   `json:"period_id,omitempty"`
	Currency             string                   `json:"currency,omitempty"`
	Unit                 string                   `json:"unit,omitempty"`
	Weight               AllocationFraction       `json:"weight"`
	Share                AllocationFraction       `json:"share"`
	RoundedAmount        *AllocationRoundedAmount `json:"rounded_amount,omitempty"`
	RoundingResidualNano int64                    `json:"rounding_residual_nano,omitempty"`
}

// AllocationRecord is an immutable, source-preserving allocation event. It is
// intentionally separate from provider money, account-window gauges, retail
// customer charges and B-leg inference quantities.
type AllocationRecord struct {
	ID       string `json:"id"`
	Version  uint64 `json:"version"`
	Revision uint64 `json:"revision,omitempty"`

	SourceSubject  metering.SubjectRef `json:"source_subject"`
	SourceBasis    ValuationBasis      `json:"source_basis"`
	SourceAmount   *metering.Decimal   `json:"source_amount,omitempty"`
	SourceQuantity *metering.Decimal   `json:"source_quantity,omitempty"`
	Currency       string              `json:"currency,omitempty"`
	Unit           string              `json:"unit,omitempty"`

	Policy                 AllocationPolicyRef      `json:"policy"`
	Operation              AllocationOperation      `json:"operation"`
	RoundingScope          RoundingScope            `json:"rounding_scope"`
	RoundingPolicy         RoundingPolicy           `json:"rounding_policy"`
	RoundingResidualPolicy AllocationResidualPolicy `json:"rounding_residual_policy"`
	RoundedSourceAmount    *AllocationRoundedAmount `json:"rounded_source_amount,omitempty"`
	RoundingResidualNano   int64                    `json:"rounding_residual_nano,omitempty"`

	SourceObservationRefs []metering.ObservationRef `json:"source_observation_refs,omitempty"`
	SourceValuationRefs   []AllocationValuationRef  `json:"source_valuation_refs,omitempty"`
	Supersedes            []AllocationRef           `json:"supersedes,omitempty"`
	Targets               []AllocationTarget        `json:"targets"`
	CreatedAt             time.Time                 `json:"created_at"`
}

// AllocationQuery is a bounded durable listing filter.
type AllocationQuery struct {
	StoreID       string
	SourceSubject *metering.SubjectRef
	TargetSubject *metering.SubjectRef
	PolicyMethod  string
	Operation     AllocationOperation
	Limit         int
	Cursor        string
}

type AllocationPage struct {
	Allocations []AllocationRecord
	NextCursor  string
}

// AllocationSupersessionStatus describes whether every supersession edge in
// the resolved allocation history names a predecessor that is present and
// verifiable. Pending is an explicit, fail-closed state; it is never treated
// as an effective or payable allocation.
type AllocationSupersessionStatus string

const (
	AllocationSupersessionResolved AllocationSupersessionStatus = "resolved"
	AllocationSupersessionPending  AllocationSupersessionStatus = "pending"
)

func (s AllocationSupersessionStatus) IsKnown() bool {
	return s == AllocationSupersessionResolved || s == AllocationSupersessionPending
}

// AllocationSupersessionResult is the deterministic view of an immutable
// allocation history. Records are canonical and identity-deduplicated;
// Effective contains only records at resolved active heads. Pending contains
// unresolved predecessor references from active records. A pending result is
// incomplete and not payable, while the original records remain unchanged.
type AllocationSupersessionResult struct {
	Status            AllocationSupersessionStatus
	Records           []AllocationRecord
	Effective         []AllocationRecord
	Pending           []AllocationRef
	PendingSupersedes []AllocationRef
	Superseded        []AllocationRef
	Complete          bool
	Payable           bool
}

// Canonical normalizes and validates the record, computes exact shares and
// applies the declared integer residual policy.
func (r AllocationRecord) Canonical() (AllocationRecord, error) {
	return normalizeAllocationEnvelope(r)
}

// Validate checks the immutable allocation envelope.
func (r AllocationRecord) Validate() error {
	_, err := r.Canonical()
	return err
}

func (r AllocationRecord) CanonicalJSON() ([]byte, error) {
	canonical, err := r.Canonical()
	if err != nil {
		return nil, err
	}
	// The alias prevents AllocationRecord.MarshalJSON from recursively calling
	// CanonicalJSON while retaining deterministic encoding/json field order.
	return json.Marshal(allocationWire(canonical))
}

func (r AllocationRecord) Fingerprint() string {
	payload, err := r.CanonicalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (r AllocationRecord) IdentityKey() string {
	if r.ID == "" || r.Version == 0 || r.SourceSubject.StoreID == "" {
		return ""
	}
	return r.SourceSubject.StoreID + ":" + r.ID + ":" + fmt.Sprint(r.Version)
}

// Clone returns an independent value copy suitable for constructing an
// additive correction or replay probe without mutating the original record.
func (r AllocationRecord) Clone() AllocationRecord {
	out := r
	if r.SourceAmount != nil {
		amount := *r.SourceAmount
		out.SourceAmount = &amount
	}
	if r.SourceQuantity != nil {
		quantity := *r.SourceQuantity
		out.SourceQuantity = &quantity
	}
	if r.RoundedSourceAmount != nil {
		rounded := *r.RoundedSourceAmount
		out.RoundedSourceAmount = &rounded
	}
	out.SourceObservationRefs = append([]metering.ObservationRef(nil), r.SourceObservationRefs...)
	out.SourceValuationRefs = append([]AllocationValuationRef(nil), r.SourceValuationRefs...)
	out.Supersedes = append([]AllocationRef(nil), r.Supersedes...)
	out.Targets = append([]AllocationTarget(nil), r.Targets...)
	for i := range out.Targets {
		if r.Targets[i].RoundedAmount != nil {
			rounded := *r.Targets[i].RoundedAmount
			out.Targets[i].RoundedAmount = &rounded
		}
	}
	return out
}

func (r AllocationRecord) MarshalJSON() ([]byte, error) { return r.CanonicalJSON() }

type allocationWire AllocationRecord

// ConserveAllocation is the named constructor used by adapters and stores.
// It returns a canonical copy; the caller's slices and pointers are untouched.
func ConserveAllocation(r AllocationRecord) (AllocationRecord, error) { return r.Canonical() }

type allocationSupersessionNode struct {
	storeID string
	id      string
	version uint64
}

func (n allocationSupersessionNode) key() string {
	return n.storeID + ":" + n.id + ":" + fmt.Sprint(n.version)
}

func (n allocationSupersessionNode) sortKey() string {
	return n.storeID + "\x00" + n.id + "\x00" + fmt.Sprint(n.version)
}

type allocationSupersessionLineage struct {
	storeID string
	id      string
}

func (l allocationSupersessionLineage) sortKey() string {
	return l.storeID + "\x00" + l.id
}

func allocationNodeForRecord(record AllocationRecord) allocationSupersessionNode {
	return allocationSupersessionNode{storeID: record.SourceSubject.StoreID, id: record.ID, version: record.Version}
}

func allocationNodeForRef(ref AllocationRef) allocationSupersessionNode {
	return allocationSupersessionNode{storeID: ref.StoreID, id: ref.AllocationID, version: ref.Version}
}

// ResolveAllocationSupersession validates and resolves an immutable
// allocation history. References to records outside the supplied history are
// retained as typed pending references. A pending record is excluded from the
// effective result until a later batch supplies its predecessor; no record is
// rewritten or inferred in the meantime.
func ResolveAllocationSupersession(records []AllocationRecord) (AllocationSupersessionResult, error) {
	known := make(map[allocationSupersessionNode]AllocationRecord, len(records))
	for _, raw := range records {
		record, err := raw.Canonical()
		if err != nil {
			return AllocationSupersessionResult{}, fmt.Errorf("economics: canonical allocation: %w", err)
		}
		node := allocationNodeForRecord(record)
		if prior, exists := known[node]; exists {
			if prior.Fingerprint() != record.Fingerprint() {
				return AllocationSupersessionResult{}, fmt.Errorf("%w: conflicting allocation identity %s", ErrAllocationSupersessionConflict, node.key())
			}
			continue
		}
		known[node] = record
	}

	nodes := make([]allocationSupersessionNode, 0, len(known))
	for node := range known {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].sortKey() < nodes[j].sortKey() })

	// Build links before payload/scope checks. This makes a cycle an explicit
	// graph error even when the records could not have been hashed as a closed
	// cycle without a forward reference.
	edges := make(map[allocationSupersessionNode][]allocationSupersessionNode, len(known))
	children := make(map[allocationSupersessionNode][]allocationSupersessionNode, len(known))
	pendingByNode := make(map[allocationSupersessionNode][]AllocationRef)
	pendingByTarget := make(map[allocationSupersessionNode]AllocationRef)
	for _, node := range nodes {
		record := known[node]
		for _, ref := range record.Supersedes {
			priorNode := allocationNodeForRef(ref)
			if prior, exists := pendingByTarget[priorNode]; exists {
				if prior.PayloadHash != ref.PayloadHash {
					return AllocationSupersessionResult{}, fmt.Errorf("%w: pending predecessor %s has conflicting payload hashes", ErrAllocationSupersessionConflict, priorNode.key())
				}
			} else {
				pendingByTarget[priorNode] = ref
			}
			edges[node] = append(edges[node], priorNode)
			children[priorNode] = append(children[priorNode], node)
			if _, exists := known[priorNode]; !exists {
				pendingByNode[node] = append(pendingByNode[node], ref)
				continue
			}
		}
	}

	if err := allocationSupersessionCycles(nodes, edges); err != nil {
		return AllocationSupersessionResult{}, err
	}

	// A known edge is usable only after both its immutable payload hash and
	// source scope have been verified. Pending edges remain in the audit graph,
	// but cannot suppress a predecessor from the effective payable view. A
	// resolved node also requires every known predecessor to be resolved: a
	// descendant of a pending record must stay pending until the entire lineage
	// is present, rather than bypassing its unresolved ancestor.
	resolvedNode := make(map[allocationSupersessionNode]bool, len(known))
	var resolveNode func(allocationSupersessionNode) bool
	resolveNode = func(node allocationSupersessionNode) bool {
		if resolved, ok := resolvedNode[node]; ok {
			return resolved
		}
		if len(pendingByNode[node]) != 0 {
			resolvedNode[node] = false
			return false
		}
		resolved := true
		for _, priorNode := range edges[node] {
			if _, exists := known[priorNode]; !exists || !resolveNode(priorNode) {
				resolved = false
				break
			}
		}
		resolvedNode[node] = resolved
		return resolved
	}
	for _, node := range nodes {
		resolveNode(node)
	}
	for _, node := range nodes {
		record := known[node]
		for _, ref := range record.Supersedes {
			priorNode := allocationNodeForRef(ref)
			prior, exists := known[priorNode]
			if !exists {
				continue
			}
			if ref.PayloadHash != prior.Fingerprint() {
				return AllocationSupersessionResult{}, fmt.Errorf("%w: predecessor %s payload hash mismatch", ErrAllocationSupersessionConflict, priorNode.key())
			}
			if !allocationSupersessionCompatible(prior, record) {
				return AllocationSupersessionResult{}, fmt.Errorf("%w: predecessor %s source scope or valuation is incompatible", ErrAllocationScopeMismatch, priorNode.key())
			}
		}
	}
	childTargets := make([]allocationSupersessionNode, 0, len(children))
	for node := range children {
		childTargets = append(childTargets, node)
	}
	sort.Slice(childTargets, func(i, j int) bool { return childTargets[i].sortKey() < childTargets[j].sortKey() })
	for _, node := range childTargets {
		children[node] = uniqueSortedAllocationNodes(children[node])
		if len(children[node]) > 1 {
			return AllocationSupersessionResult{}, fmt.Errorf("%w: predecessor %s has active successors %s and %s", ErrAllocationSupersessionFork, node.key(), children[node][0].key(), children[node][1].key())
		}
	}

	// Only a fully resolved successor can retire its predecessor. This keeps a
	// predecessor payable while a correction that also names an absent parent is
	// still pending, and permits deterministic convergence when that parent is
	// appended later.
	supersededNodes := make(map[allocationSupersessionNode]bool, len(known))
	for _, node := range nodes {
		if !resolvedNode[node] {
			continue
		}
		for _, priorNode := range edges[node] {
			supersededNodes[priorNode] = true
		}
	}

	pending := make([]AllocationRef, 0)
	var collectPending func(allocationSupersessionNode, map[allocationSupersessionNode]struct{})
	collectPending = func(node allocationSupersessionNode, visiting map[allocationSupersessionNode]struct{}) {
		if _, seen := visiting[node]; seen {
			return
		}
		visiting[node] = struct{}{}
		pending = append(pending, pendingByNode[node]...)
		for _, priorNode := range edges[node] {
			if _, exists := known[priorNode]; exists && !resolvedNode[priorNode] {
				collectPending(priorNode, visiting)
			}
		}
		delete(visiting, node)
	}
	for _, node := range nodes {
		if supersededNodes[node] || resolvedNode[node] {
			continue
		}
		collectPending(node, make(map[allocationSupersessionNode]struct{}))
	}
	sort.Slice(pending, func(i, j int) bool { return allocationRefSortKey(pending[i]) < allocationRefSortKey(pending[j]) })
	pending = uniqueSortedAllocationRefs(pending)
	activeHeads := make(map[allocationSupersessionLineage][]allocationSupersessionNode)
	for _, node := range nodes {
		if supersededNodes[node] || !resolvedNode[node] {
			continue
		}
		lineageKey := allocationSupersessionLineage{storeID: node.storeID, id: node.id}
		activeHeads[lineageKey] = append(activeHeads[lineageKey], node)
	}
	lineageKeys := make([]allocationSupersessionLineage, 0, len(activeHeads))
	for lineageKey := range activeHeads {
		lineageKeys = append(lineageKeys, lineageKey)
	}
	sort.Slice(lineageKeys, func(i, j int) bool { return lineageKeys[i].sortKey() < lineageKeys[j].sortKey() })
	for _, lineageKey := range lineageKeys {
		heads := activeHeads[lineageKey]
		if len(heads) < 2 {
			continue
		}
		sort.Slice(heads, func(i, j int) bool { return heads[i].sortKey() < heads[j].sortKey() })
		return AllocationSupersessionResult{}, fmt.Errorf("%w: lineage store=%q id=%q has active heads %s and %s", ErrAllocationSupersessionFork, lineageKey.storeID, lineageKey.id, heads[0].key(), heads[1].key())
	}

	result := AllocationSupersessionResult{
		Status:            AllocationSupersessionResolved,
		Records:           make([]AllocationRecord, 0, len(nodes)),
		Effective:         make([]AllocationRecord, 0, len(nodes)),
		Pending:           pending,
		PendingSupersedes: append([]AllocationRef(nil), pending...),
		Complete:          len(pending) == 0,
		Payable:           len(pending) == 0,
	}
	for _, node := range nodes {
		record := known[node]
		result.Records = append(result.Records, record)
		if supersededNodes[node] || !resolvedNode[node] {
			continue
		}
		result.Effective = append(result.Effective, record)
	}
	if len(pending) != 0 {
		result.Status = AllocationSupersessionPending
	}
	for _, node := range nodes {
		if !supersededNodes[node] {
			continue
		}
		for _, child := range children[node] {
			if resolvedNode[child] {
				record := known[child]
				for _, ref := range record.Supersedes {
					if allocationNodeForRef(ref) == node {
						result.Superseded = append(result.Superseded, ref)
					}
				}
			}
		}
	}
	sort.Slice(result.Superseded, func(i, j int) bool {
		return allocationRefSortKey(result.Superseded[i]) < allocationRefSortKey(result.Superseded[j])
	})
	return result, nil
}

// ValidateAllocationSupersessionGraph validates immutable links while
// allowing absent predecessors to remain pending. Callers that need the
// effective/payable view should use ResolveAllocationSupersession.
func ValidateAllocationSupersessionGraph(records []AllocationRecord) error {
	_, err := ResolveAllocationSupersession(records)
	return err
}

func allocationSupersessionCycles(nodes []allocationSupersessionNode, edges map[allocationSupersessionNode][]allocationSupersessionNode) error {
	state := make(map[allocationSupersessionNode]uint8, len(nodes))
	var visit func(allocationSupersessionNode) error
	visit = func(node allocationSupersessionNode) error {
		switch state[node] {
		case 1:
			return fmt.Errorf("%w: node %s is revisited", ErrAllocationSupersessionCycle, node.key())
		case 2:
			return nil
		}
		state[node] = 1
		parents := append([]allocationSupersessionNode(nil), edges[node]...)
		sort.Slice(parents, func(i, j int) bool { return parents[i].sortKey() < parents[j].sortKey() })
		for _, parent := range parents {
			if err := visit(parent); err != nil {
				return err
			}
		}
		state[node] = 2
		return nil
	}
	for _, node := range nodes {
		if err := visit(node); err != nil {
			return err
		}
	}
	return nil
}

func allocationSupersessionCompatible(prior, successor AllocationRecord) bool {
	if prior.SourceSubject != successor.SourceSubject || prior.SourceBasis != successor.SourceBasis {
		return false
	}
	// The policy reference is part of the frozen allocation source plane. A
	// correction/replacement may change the amount or target weights, but it
	// cannot silently move the same source lineage to another policy/version.
	if prior.Policy != successor.Policy {
		return false
	}
	if prior.RoundingScope != successor.RoundingScope || prior.RoundingPolicy != successor.RoundingPolicy || prior.RoundingResidualPolicy != successor.RoundingResidualPolicy {
		return false
	}
	if (prior.SourceAmount == nil) != (successor.SourceAmount == nil) {
		return false
	}
	return prior.Currency == successor.Currency && prior.Unit == successor.Unit
}

func allocationRefSortKey(ref AllocationRef) string {
	return ref.StoreID + "\x00" + ref.AllocationID + "\x00" + fmt.Sprint(ref.Version) + "\x00" + ref.PayloadHash
}

func uniqueSortedAllocationNodes(nodes []allocationSupersessionNode) []allocationSupersessionNode {
	if len(nodes) < 2 {
		return nodes
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].sortKey() < nodes[j].sortKey() })
	out := nodes[:1]
	for _, node := range nodes[1:] {
		if node != out[len(out)-1] {
			out = append(out, node)
		}
	}
	return out
}

func uniqueSortedAllocationRefs(refs []AllocationRef) []AllocationRef {
	if len(refs) < 2 {
		return refs
	}
	out := refs[:1]
	for _, ref := range refs[1:] {
		if ref != out[len(out)-1] {
			out = append(out, ref)
		}
	}
	return out
}

func normalizeAllocationEnvelope(r AllocationRecord) (AllocationRecord, error) {
	out := r
	if err := ValidateSafeRef("allocation id", out.ID); err != nil {
		return AllocationRecord{}, fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
	}
	if out.Version == 0 {
		return AllocationRecord{}, fmt.Errorf("%w: version required", ErrInvalidAllocation)
	}
	if out.Revision == 0 {
		out.Revision = 1
	}
	if err := out.SourceSubject.Validate(); err != nil {
		return AllocationRecord{}, fmt.Errorf("%w: %v: %v", ErrAllocationInvalidSource, metering.ErrInvalidSubject, err)
	}
	out.SourceSubject = canonicalAllocationSubject(out.SourceSubject)
	switch out.SourceSubject.Kind {
	case metering.SubjectResource, metering.SubjectStatementLine, metering.SubjectProviderCharge:
	default:
		return AllocationRecord{}, fmt.Errorf("%w: source subject kind %q cannot own allocated cost", ErrAllocationInvalidSource, out.SourceSubject.Kind)
	}
	if !out.SourceBasis.IsKnown() || out.SourceBasis == BasisProviderUnitDebit {
		return AllocationRecord{}, fmt.Errorf("%w: source basis %q cannot carry explicit local allocation", ErrAllocationInvalidSource, out.SourceBasis)
	}
	if out.SourceAmount != nil && out.SourceQuantity != nil {
		return AllocationRecord{}, fmt.Errorf("%w: source amount and quantity are mutually exclusive", ErrInvalidAllocation)
	}
	if out.SourceAmount == nil && out.SourceQuantity == nil {
		return AllocationRecord{}, fmt.Errorf("%w: source amount or exact source quantity required", ErrInvalidAllocation)
	}
	if out.SourceAmount != nil {
		if out.SourceBasis == BasisProviderQuantityLocal {
			return AllocationRecord{}, fmt.Errorf("%w: provider quantity basis requires an exact source quantity", ErrAllocationInvalidSource)
		}
		normalized, err := out.SourceAmount.Normalize()
		if err != nil {
			return AllocationRecord{}, fmt.Errorf("%w: source amount: %v", ErrInvalidAllocation, err)
		}
		out.SourceAmount = &normalized
		currency, err := NormalizeCurrency(out.Currency)
		if err != nil {
			return AllocationRecord{}, fmt.Errorf("%w: source currency: %v", ErrInvalidAllocation, err)
		}
		out.Currency = currency
		if out.Unit != "" {
			return AllocationRecord{}, fmt.Errorf("%w: monetary source cannot carry a unit", ErrInvalidAllocation)
		}
	} else {
		normalized, err := out.SourceQuantity.Normalize()
		if err != nil {
			return AllocationRecord{}, fmt.Errorf("%w: source quantity: %v", ErrInvalidAllocation, err)
		}
		out.SourceQuantity = &normalized
		if out.Currency != "" {
			return AllocationRecord{}, fmt.Errorf("%w: quantity source cannot carry currency", ErrInvalidAllocation)
		}
		if err := ValidateSafeRef("allocation unit", out.Unit); err != nil {
			return AllocationRecord{}, fmt.Errorf("%w: %v", ErrInvalidAllocation, err)
		}
		if (out.Unit == metering.UnitToken || out.Unit == metering.UnitCount) && normalized.Scale != 0 {
			return AllocationRecord{}, fmt.Errorf("%w: source quantity for %s must be an exact integer", ErrInvalidAllocation, out.Unit)
		}
	}
	policy, err := out.Policy.normalize()
	if err != nil {
		return AllocationRecord{}, err
	}
	out.Policy = policy
	if !out.Operation.IsKnown() {
		return AllocationRecord{}, fmt.Errorf("%w: unknown operation %q", ErrInvalidAllocation, out.Operation)
	}
	if !out.RoundingScope.IsKnown() {
		return AllocationRecord{}, fmt.Errorf("%w: unknown rounding scope %q", ErrInvalidAllocation, out.RoundingScope)
	}
	if !out.RoundingPolicy.IsKnown() || out.RoundingPolicy == RoundingUnspecified && out.SourceAmount != nil {
		return AllocationRecord{}, fmt.Errorf("%w: explicit rounding policy required", ErrInvalidAllocation)
	}
	if !out.RoundingResidualPolicy.IsKnown() {
		return AllocationRecord{}, fmt.Errorf("%w: explicit rounding residual policy required", ErrInvalidAllocation)
	}
	if err := allocationSignCheck(out); err != nil {
		return AllocationRecord{}, err
	}
	requiresSupersession := out.Operation == AllocationOperationCorrection || out.Operation == AllocationOperationReplacement
	if requiresSupersession && len(out.Supersedes) == 0 {
		return AllocationRecord{}, ErrAllocationRevisionRequired
	}
	if len(out.Targets) == 0 || len(out.Targets) > MaxAllocationTargets {
		return AllocationRecord{}, fmt.Errorf("%w: one to %d targets required", ErrInvalidAllocation, MaxAllocationTargets)
	}
	if len(out.SourceObservationRefs) > MaxAllocationRefs || len(out.SourceValuationRefs) > MaxAllocationRefs || len(out.Supersedes) > MaxAllocationRefs {
		return AllocationRecord{}, fmt.Errorf("%w: reference limit exceeded", ErrInvalidAllocation)
	}
	if err := validateObservationRefCollection(out.SourceObservationRefs, out.SourceSubject.StoreID, "source"); err != nil {
		return AllocationRecord{}, err
	}
	if err := normalizeAllocationRefs(out); err != nil {
		return AllocationRecord{}, err
	}
	out.SourceObservationRefs = append([]metering.ObservationRef(nil), out.SourceObservationRefs...)
	out.SourceValuationRefs = append([]AllocationValuationRef(nil), out.SourceValuationRefs...)
	out.Supersedes = append([]AllocationRef(nil), out.Supersedes...)
	sort.SliceStable(out.SourceObservationRefs, func(i, j int) bool {
		if out.SourceObservationRefs[i].StoreID != out.SourceObservationRefs[j].StoreID {
			return out.SourceObservationRefs[i].StoreID < out.SourceObservationRefs[j].StoreID
		}
		if out.SourceObservationRefs[i].ObservationID != out.SourceObservationRefs[j].ObservationID {
			return out.SourceObservationRefs[i].ObservationID < out.SourceObservationRefs[j].ObservationID
		}
		return out.SourceObservationRefs[i].Revision < out.SourceObservationRefs[j].Revision
	})
	sort.SliceStable(out.SourceValuationRefs, func(i, j int) bool {
		if out.SourceValuationRefs[i].StoreID != out.SourceValuationRefs[j].StoreID {
			return out.SourceValuationRefs[i].StoreID < out.SourceValuationRefs[j].StoreID
		}
		if out.SourceValuationRefs[i].ValuationID != out.SourceValuationRefs[j].ValuationID {
			return out.SourceValuationRefs[i].ValuationID < out.SourceValuationRefs[j].ValuationID
		}
		return out.SourceValuationRefs[i].Version < out.SourceValuationRefs[j].Version
	})
	sort.SliceStable(out.Supersedes, func(i, j int) bool {
		if out.Supersedes[i].StoreID != out.Supersedes[j].StoreID {
			return out.Supersedes[i].StoreID < out.Supersedes[j].StoreID
		}
		if out.Supersedes[i].AllocationID != out.Supersedes[j].AllocationID {
			return out.Supersedes[i].AllocationID < out.Supersedes[j].AllocationID
		}
		return out.Supersedes[i].Version < out.Supersedes[j].Version
	})
	if out.SourceAmount == nil && (out.RoundedSourceAmount != nil || out.RoundingResidualNano != 0) {
		return AllocationRecord{}, fmt.Errorf("%w: quantity allocation cannot carry rounded money", ErrInvalidAllocation)
	}
	out.RoundedSourceAmount = nil
	out.RoundingResidualNano = 0

	out.Targets = append([]AllocationTarget(nil), out.Targets...)
	var weightSum big.Rat
	seenIDs := make(map[string]struct{}, len(out.Targets))
	seenSubjects := make(map[string]struct{}, len(out.Targets))
	for i := range out.Targets {
		target, err := normalizeAllocationTarget(out.Targets[i], out)
		if err != nil {
			return AllocationRecord{}, err
		}
		if _, exists := seenIDs[target.TargetID]; exists {
			return AllocationRecord{}, fmt.Errorf("%w: duplicate target id %q", ErrAllocationTargetConflict, target.TargetID)
		}
		seenIDs[target.TargetID] = struct{}{}
		targetKey := "unallocated"
		if !target.Unallocated {
			payload, _ := json.Marshal(target.Target)
			targetKey = string(payload)
		}
		if _, exists := seenSubjects[targetKey]; exists {
			return AllocationRecord{}, fmt.Errorf("%w: duplicate target subject", ErrAllocationTargetConflict)
		}
		seenSubjects[targetKey] = struct{}{}
		fraction, _ := target.Weight.Rat()
		weightSum.Add(&weightSum, fraction)
		target.Share = target.Weight
		out.Targets[i] = target
	}
	if weightSum.Cmp(big.NewRat(1, 1)) != 0 {
		return AllocationRecord{}, fmt.Errorf("%w: sum=%s", ErrAllocationNotConserved, weightSum.RatString())
	}
	// Sort before residual assignment so replay does not depend on input order.
	sort.SliceStable(out.Targets, func(i, j int) bool {
		return allocationTargetSortKey(out.Targets[i]) < allocationTargetSortKey(out.Targets[j])
	})
	if err := applyAllocationRounding(&out); err != nil {
		return AllocationRecord{}, err
	}
	if out.CreatedAt.IsZero() {
		return AllocationRecord{}, fmt.Errorf("%w: created_at is required for deterministic identity", ErrInvalidAllocation)
	}
	out.CreatedAt = out.CreatedAt.UTC()
	return out, nil
}

func normalizeAllocationRefs(r AllocationRecord) error {
	seenObs := make(map[string]struct{}, len(r.SourceObservationRefs))
	for _, ref := range r.SourceObservationRefs {
		key := ref.StoreID + ":" + ref.ObservationID + ":" + fmt.Sprint(ref.Revision)
		if _, ok := seenObs[key]; ok {
			return fmt.Errorf("%w: duplicate source observation ref", ErrAllocationTargetConflict)
		}
		seenObs[key] = struct{}{}
	}
	seenValuations := make(map[string]struct{}, len(r.SourceValuationRefs))
	for _, ref := range r.SourceValuationRefs {
		if err := ref.normalize(); err != nil {
			return err
		}
		key := ref.StoreID + ":" + ref.ValuationID + ":" + fmt.Sprint(ref.Version)
		if _, ok := seenValuations[key]; ok {
			return fmt.Errorf("%w: duplicate source valuation ref", ErrAllocationTargetConflict)
		}
		seenValuations[key] = struct{}{}
		if ref.StoreID != r.SourceSubject.StoreID {
			return fmt.Errorf("%w: source valuation ref store mismatch", ErrAllocationScopeMismatch)
		}
	}
	seenSupersedes := make(map[string]struct{}, len(r.Supersedes))
	for _, ref := range r.Supersedes {
		if err := ref.normalize(); err != nil {
			return err
		}
		if ref.AllocationID == r.ID && ref.Version == r.Version {
			return fmt.Errorf("%w: allocation cannot supersede itself", ErrAllocationRevisionRequired)
		}
		if ref.StoreID != r.SourceSubject.StoreID {
			return fmt.Errorf("%w: superseded allocation store mismatch", ErrAllocationScopeMismatch)
		}
		key := ref.StoreID + ":" + ref.AllocationID + ":" + fmt.Sprint(ref.Version)
		if _, ok := seenSupersedes[key]; ok {
			return fmt.Errorf("%w: duplicate superseded allocation ref", ErrAllocationTargetConflict)
		}
		seenSupersedes[key] = struct{}{}
	}
	return nil
}

func allocationSignCheck(r AllocationRecord) error {
	value := r.SourceAmount
	if value == nil {
		value = r.SourceQuantity
	}
	normalized, err := value.Normalize()
	if err != nil {
		return fmt.Errorf("%w: source value: %v", ErrInvalidAllocation, err)
	}
	negative := strings.HasPrefix(normalized.Coefficient, "-") && normalized.Coefficient != "-0"
	positive := normalized.Coefficient != "0" && !negative
	switch r.Operation {
	case AllocationOperationAllocate:
		if negative {
			return ErrAllocationSignMismatch
		}
	case AllocationOperationRefund:
		if positive || !negative {
			return ErrAllocationSignMismatch
		}
	case AllocationOperationZero:
		if normalized.Coefficient != "0" {
			return ErrAllocationSignMismatch
		}
	}
	return nil
}

func normalizeAllocationTarget(target AllocationTarget, record AllocationRecord) (AllocationTarget, error) {
	if err := ValidateSafeRef("allocation target id", target.TargetID); err != nil {
		return AllocationTarget{}, fmt.Errorf("%w: %v", ErrAllocationTargetConflict, err)
	}
	weight, err := target.Weight.Normalize()
	if err != nil {
		return AllocationTarget{}, err
	}
	target.Weight = weight
	target.Share = weight
	if record.SourceAmount == nil && (target.RoundedAmount != nil || target.RoundingResidualNano != 0) {
		return AllocationTarget{}, fmt.Errorf("%w: quantity allocation cannot carry rounded money", ErrInvalidAllocation)
	}
	target.RoundedAmount = nil
	target.RoundingResidualNano = 0
	if target.Unallocated {
		if target.Target != (metering.SubjectRef{}) || target.Informational {
			return AllocationTarget{}, fmt.Errorf("%w: unallocated target cannot carry subject or informational lineage", ErrAllocationTargetConflict)
		}
	} else {
		if err := target.Target.Validate(); err != nil {
			return AllocationTarget{}, fmt.Errorf("%w: target subject: %v", ErrAllocationTargetConflict, err)
		}
		target.Target = canonicalAllocationSubject(target.Target)
		if target.Target.Kind == metering.SubjectAccountWindow || target.Target.Kind == metering.SubjectProviderDebit {
			return AllocationTarget{}, fmt.Errorf("%w: account gauge/provider debit cannot be an allocation target", ErrAllocationScopeMismatch)
		}
		if target.Target.StoreID != record.SourceSubject.StoreID {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		if record.SourceSubject.TenantID != "" && target.Target.TenantID != "" && target.Target.TenantID != record.SourceSubject.TenantID {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		if record.SourceSubject.TenantID == "" && target.Target.TenantID != "" {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		if target.Target.TenantID == "" {
			target.Target.TenantID = record.SourceSubject.TenantID
		}
		if record.SourceSubject.PeriodID != "" && target.Target.PeriodID != "" && target.Target.PeriodID != record.SourceSubject.PeriodID {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		if record.SourceSubject.PeriodID == "" && target.Target.PeriodID != "" {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		if target.Target.PeriodID == "" {
			target.Target.PeriodID = record.SourceSubject.PeriodID
		}
		if record.SourceSubject.AccountID != "" && target.Target.AccountID != "" && target.Target.AccountID != record.SourceSubject.AccountID {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		if record.SourceSubject.AccountID == "" && target.Target.AccountID != "" {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		if target.AccountID != "" && target.AccountID != record.SourceSubject.AccountID {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		if target.Target.AccountID == "" {
			target.Target.AccountID = record.SourceSubject.AccountID
		}
	}
	if target.AccountID == "" {
		target.AccountID = record.SourceSubject.AccountID
	} else if target.AccountID != record.SourceSubject.AccountID {
		return AllocationTarget{}, ErrAllocationScopeMismatch
	}
	if target.PeriodID != "" && target.PeriodID != record.SourceSubject.PeriodID {
		return AllocationTarget{}, ErrAllocationScopeMismatch
	}
	target.PeriodID = record.SourceSubject.PeriodID
	if record.SourceAmount != nil {
		if target.Currency != "" {
			currency, err := NormalizeCurrency(target.Currency)
			if err != nil {
				return AllocationTarget{}, fmt.Errorf("%w: target currency: %v", ErrAllocationScopeMismatch, err)
			}
			target.Currency = currency
		}
		if target.Unit != "" || (target.Currency != "" && target.Currency != record.Currency) {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		target.Currency = record.Currency
	} else {
		if target.Unit != "" {
			if err := ValidateSafeRef("allocation target unit", target.Unit); err != nil {
				return AllocationTarget{}, fmt.Errorf("%w: %v", ErrAllocationScopeMismatch, err)
			}
		}
		if target.Currency != "" || (target.Unit != "" && target.Unit != record.Unit) {
			return AllocationTarget{}, ErrAllocationScopeMismatch
		}
		target.Unit = record.Unit
	}
	return target, nil
}

func canonicalAllocationSubject(subject metering.SubjectRef) metering.SubjectRef {
	if !subject.ResetAt.IsZero() {
		subject.ResetAt = subject.ResetAt.UTC()
	}
	if !subject.StartAt.IsZero() {
		subject.StartAt = subject.StartAt.UTC()
	}
	if !subject.EndAt.IsZero() {
		subject.EndAt = subject.EndAt.UTC()
	}
	return subject
}

func allocationTargetSortKey(target AllocationTarget) string {
	if target.Unallocated {
		return "1:" + target.TargetID
	}
	payload, _ := json.Marshal(target.Target)
	return "0:" + target.TargetID + ":" + string(payload)
}

func applyAllocationRounding(record *AllocationRecord) error {
	if record.SourceAmount == nil {
		record.RoundingResidualNano = 0
		return nil
	}
	sourceRat, err := record.SourceAmount.ToRat()
	if err != nil {
		return err
	}
	nanoScale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(metering.LedgerNanoScale)), nil))
	sourceNano := new(big.Rat).Mul(sourceRat, nanoScale)
	sourceRounded, err := RoundToInt64(sourceNano, record.RoundingPolicy)
	if err != nil {
		return err
	}
	record.RoundedSourceAmount = &AllocationRoundedAmount{NanoUnits: sourceRounded, Currency: record.Currency, Present: true, Policy: record.RoundingPolicy}
	total := int64(0)
	for i := range record.Targets {
		share, _ := record.Targets[i].Share.Rat()
		targetNano := new(big.Rat).Mul(sourceRat, share)
		targetNano.Mul(targetNano, nanoScale)
		rounded, err := RoundToInt64(targetNano, record.RoundingPolicy)
		if err != nil {
			return err
		}
		record.Targets[i].RoundedAmount = &AllocationRoundedAmount{NanoUnits: rounded, Currency: record.Currency, Present: true, Policy: record.RoundingPolicy}
		total, err = addAllocationInt64(total, rounded)
		if err != nil {
			return err
		}
	}
	residual, err := subtractAllocationInt64(sourceRounded, total)
	if err != nil {
		return err
	}
	record.RoundingResidualNano = residual
	if residual == 0 {
		return nil
	}
	index := -1
	switch record.RoundingResidualPolicy {
	case AllocationResidualReject:
		return ErrAllocationResidualUnassigned
	case AllocationResidualToUnallocated:
		for i := range record.Targets {
			if record.Targets[i].Unallocated {
				if index != -1 {
					return ErrAllocationTargetConflict
				}
				index = i
			}
		}
		if index == -1 {
			return ErrAllocationResidualUnassigned
		}
	case AllocationResidualToLastTarget:
		for i := range record.Targets {
			if !record.Targets[i].Unallocated {
				index = i
			}
		}
		if index == -1 {
			index = len(record.Targets) - 1
		}
	default:
		return fmt.Errorf("%w: unknown residual policy %q", ErrInvalidAllocation, record.RoundingResidualPolicy)
	}
	updated, err := addAllocationInt64(record.Targets[index].RoundedAmount.NanoUnits, residual)
	if err != nil {
		return err
	}
	record.Targets[index].RoundedAmount.NanoUnits = updated
	record.Targets[index].RoundingResidualNano = residual
	return nil
}

func addAllocationInt64(a, b int64) (int64, error) {
	const maxInt64 = int64(^uint64(0) >> 1)
	const minInt64 = -maxInt64 - 1
	if (b > 0 && a > maxInt64-b) || (b < 0 && a < minInt64-b) {
		return 0, fmt.Errorf("%w: rounded allocation overflow", ErrInvalidAllocation)
	}
	return a + b, nil
}

func subtractAllocationInt64(a, b int64) (int64, error) {
	left := new(big.Int).SetInt64(a)
	left.Sub(left, new(big.Int).SetInt64(b))
	if !left.IsInt64() {
		return 0, fmt.Errorf("%w: rounded allocation overflow", ErrInvalidAllocation)
	}
	return left.Int64(), nil
}
