package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

var (
	// ErrProviderCostRevisionInvalid identifies malformed revision posting
	// input. Provider work is immutable and must be rejected before a journal
	// transaction is opened when its identity is not complete.
	ErrProviderCostRevisionInvalid = errors.New("billing: invalid provider cost revision")
	// ErrProviderCostRevisionConflict identifies a same immutable revision
	// identity whose valuation, subject, or selected amount differs from the
	// durable first result.
	ErrProviderCostRevisionConflict = errors.New("billing: provider cost revision conflict")
	// ErrProviderCostRevisionFence identifies a stale head transition. Stores
	// may retry the enclosing transaction, but must never overwrite a newer
	// selected-cost head.
	ErrProviderCostRevisionFence = errors.New("billing: provider cost revision fence conflict")
	// ErrProviderCostHeadNotFound identifies a requested current provider-cost
	// head that has not yet been created.
	ErrProviderCostHeadNotFound = errors.New("billing: provider cost head not found")
	// ErrProviderCostRevisionAuthority identifies a provider-cost revision that
	// claims payable authority without the matching typed provider evidence.
	ErrProviderCostRevisionAuthority = errors.New("billing: provider cost revision authority rejected")
)

// ProviderCostRevisionAuthorityError is returned when a provider-cost
// revision crosses the monetary posting boundary without the required
// provider-origin, observed evidence. Field and Expected contain bounded
// schema labels; Value is restricted to a bounded enum/absence description so
// malformed input cannot make secrets part of an accounting error.
type ProviderCostRevisionAuthorityError struct {
	Field    string
	Value    string
	Expected string
}

func (e *ProviderCostRevisionAuthorityError) Error() string {
	if e == nil {
		return ErrProviderCostRevisionAuthority.Error()
	}
	if e.Expected == "" {
		return fmt.Sprintf("%s: %s", ErrProviderCostRevisionAuthority, e.Field)
	}
	// Do not put even bounded provider-supplied values into the ordinary error
	// string. Structured callers can inspect Value after errors.As without
	// accidentally forwarding untrusted provenance text to logs.
	return fmt.Sprintf("%s: %s requires %s", ErrProviderCostRevisionAuthority, e.Field, e.Expected)
}

// Unwrap preserves both the specific authority classification and the generic
// malformed-revision classification used by older callers.
func (e *ProviderCostRevisionAuthorityError) Unwrap() []error {
	return []error{ErrProviderCostRevisionAuthority, ErrProviderCostRevisionInvalid}
}

func providerCostRevisionAuthorityError(field, value, expected string) error {
	return &ProviderCostRevisionAuthorityError{
		Field: field, Value: boundedAuthorityValue(value), Expected: expected,
	}
}

func boundedAuthorityValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "<absent>"
	}
	if len(value) > 64 || strings.ContainsAny(value, "\r\n") {
		return "<redacted>"
	}
	switch value {
	case metering.OriginLocal, metering.OriginProvider, metering.OriginStatement,
		metering.AcquisitionLocalTokenizer, metering.AcquisitionLocalTransport,
		metering.AcquisitionLocalEstimator, metering.AcquisitionProviderCountAPI,
		metering.AcquisitionProviderResponse, metering.AcquisitionProviderHeader,
		metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim,
		metering.AuthorityEstimatedClaim, metering.AuthorityVerifiedStatement,
		metering.AuthorityUnavailableClaim, string(metering.PerspectiveCustomer),
		string(metering.PerspectiveOperator), string(metering.PerspectiveNone),
		string(metering.BoundaryFrontendIngress), string(metering.BoundaryBackendIngress),
		string(metering.BoundaryBackendEgress), string(metering.BoundaryFrontendEgress),
		string(metering.LifecycleLogicalRequest), string(metering.LifecycleBackendAttempt),
		string(metering.LifecycleAuxiliaryRequest), string(metering.PaymentPartyUnknown),
		string(metering.PaymentPartyUnallocated), string(economics.BasisLocalExpected),
		string(economics.BasisProviderQuantityLocal), string(economics.BasisProviderUnitDebit),
		string(economics.BasisProviderReported), string(economics.BasisStatementReported),
		string(economics.BasisCustomerPolicy), string(economics.BasisAllocatedCost),
		string(metering.SubjectBLeg), string(metering.SubjectProviderCharge),
		"true", "false", "mismatch", "references-only", "non-operator or incomplete",
		"does not match provider charges":
		return value
	default:
		return "<redacted>"
	}
}

// ProviderCostRevisionInput is the durable-store boundary for one selected
// provider-cost revision. Cost is the all-attributable B-leg COGS result; it
// is intentionally separate from customer retail selection. Amount and
// Revision are compatibility conveniences for direct adapters: normalized
// callers should prefer Cost and EvidenceRevision.
type ProviderCostRevisionInput struct {
	AccountID string
	CallID    BillingCallID
	Subject   metering.SubjectRef
	// ALegID and BLegID are compact compatibility fields. Subject is the
	// authoritative tagged lineage when it is supplied; these fields only fill
	// its corresponding empty ancestry fields at normalization.
	ALegID           string
	BLegID           string
	HeadKey          string
	EvidenceRevision uint64
	Revision         uint64
	InputSetHash     string
	ValuationID      string
	Cost             OperatorCOGSResult
	// Evidence is the immutable provider-neutral rating input that authorizes
	// a payable operator COGS selection. It is intentionally retained at this
	// boundary instead of reducing authority to a caller-controlled boolean.
	Evidence      economics.PostUsageRatingInput
	Amount        Money
	AmountPresent bool
	Authoritative bool
	// PostingOwner selects the B1 pin owner for the provider charge fence.
	// Empty preserves the legacy V1 default for backward compatibility.
	// Draining requires a classified V1 pin plus matching claim metadata;
	// v2_active permits only V2.
	PostingOwner string
	// Claim carries the B2a worker-claim metadata (owner/epoch) captured at
	// claim time. When present, posting validates it against the current
	// marker and pin to close TOCTOU between claim and posting. Nil preserves
	// legacy direct calls in v1_active/shadow; draining fences unpinned/stale
	// work even without a claim, and B2b2 draining requires a matching claim
	// for new postings.
	Claim *CutoverClaimMetadata
}

// OperatorCostRevisionInput is the descriptive operator-side spelling.
type OperatorCostRevisionInput = ProviderCostRevisionInput

// ProviderCostRevisionResult reports one fenced current-head transition. A
// negative Delta is a reversal of previously accrued COGS; it never mutates a
// customer balance. Ignored marks a complete but non-payable payer selection
// (for example BYOK/customer-payable provider charges).
type ProviderCostRevisionResult struct {
	AccountID      string
	CallID         BillingCallID
	HeadKey        string
	ALegID         string
	BLegID         string
	Revision       uint64
	PreviousAmount Money
	CurrentAmount  Money
	Delta          Money
	Posting        Posting
	Applied        bool
	Replayed       bool
	Stale          bool
	Ignored        bool
}

// OperatorCostRevisionResult is the descriptive operator-side spelling.
type OperatorCostRevisionResult = ProviderCostRevisionResult

// ProviderCostHead is the rebuildable selected-cost pointer. Immutable work,
// valuation and journal rows remain the audit history; HeadVersion/Fence
// protect this pointer from late or concurrent workers.
type ProviderCostHead struct {
	AccountID        string
	CallID           BillingCallID
	HeadKey          string
	Subject          metering.SubjectRef
	EvidenceRevision uint64
	InputSetHash     string
	ValuationID      string
	CurrentAmount    Money
	HeadVersion      uint64
	Fence            uint64
	LastOperationKey string
	// OriginalTransactionID is the first durable provider-cost journal in this
	// head's correction group. LastTransactionID is the most recent journal and
	// is the target for the next chained adjustment.
	OriginalTransactionID string
	LastTransactionID     string
	UpdatedAt             time.Time
}

// OperatorCostHead is the descriptive operator-side spelling.
type OperatorCostHead = ProviderCostHead

// ProviderCostRevisionStore owns the only monetary writer for provider COGS
// revisions. Implementations must atomically fence the head and journal delta
// and must not lock or update customer balance state for pure provider COGS.
type ProviderCostRevisionStore interface {
	ApplyProviderCostRevision(context.Context, ProviderCostRevisionInput) (ProviderCostRevisionResult, error)
}

// OperatorCostRevisionStore is the descriptive operator-side spelling.
type OperatorCostRevisionStore = ProviderCostRevisionStore

// ProviderCostHeadReader reads the current selected-cost pointer without
// changing any accounting state.
type ProviderCostHeadReader interface {
	GetProviderCostHead(context.Context, string, BillingCallID, string) (ProviderCostHead, error)
}

func (in ProviderCostRevisionInput) normalized() (ProviderCostRevisionInput, error) {
	out := in
	out.AccountID = strings.TrimSpace(out.AccountID)
	out.HeadKey = strings.TrimSpace(out.HeadKey)
	out.InputSetHash = strings.TrimSpace(out.InputSetHash)
	out.ValuationID = strings.TrimSpace(out.ValuationID)
	out.Evidence = out.Evidence.Clone()
	if out.AccountID == "" {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: account id is required", ErrProviderCostRevisionInvalid)
	}
	if err := out.CallID.Validate(); err != nil {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: call id: %v", ErrProviderCostRevisionInvalid, err)
	}
	if out.Subject.Kind == "" {
		// Direct store adapters may supply the compact lineage fields and let the
		// normalizer construct the tagged B-leg subject.
		out.Subject = metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: out.Subject.StoreID, AccountID: out.AccountID,
			ALegID: out.ALegID, BillingCallID: out.CallID.String(), BLegID: out.BLegID,
		}
	} else {
		if out.ALegID != "" {
			if out.Subject.ALegID != "" && out.Subject.ALegID != out.ALegID {
				return ProviderCostRevisionInput{}, fmt.Errorf("%w: subject A-leg differs from compatibility field", ErrProviderCostRevisionInvalid)
			}
			out.Subject.ALegID = out.ALegID
		}
		if out.BLegID != "" {
			if out.Subject.BLegID != "" && out.Subject.BLegID != out.BLegID {
				return ProviderCostRevisionInput{}, fmt.Errorf("%w: subject B-leg differs from compatibility field", ErrProviderCostRevisionInvalid)
			}
			out.Subject.BLegID = out.BLegID
		}
	}
	if err := out.Subject.Validate(); err != nil {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: subject: %v", ErrProviderCostRevisionInvalid, err)
	}
	if out.Subject.Kind != metering.SubjectBLeg && out.Subject.Kind != metering.SubjectProviderCharge {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: request-scoped B-leg/provider-charge subject required", ErrProviderCostRevisionInvalid)
	}
	if out.Subject.BillingCallID != "" && out.Subject.BillingCallID != out.CallID.String() {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: subject billing call differs from call id", ErrProviderCostRevisionInvalid)
	}
	if out.Subject.CallID != "" && out.Subject.CallID != out.CallID.String() {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: subject call differs from call id", ErrProviderCostRevisionInvalid)
	}
	if out.Subject.AccountID != "" && out.Subject.AccountID != out.AccountID {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: subject account differs from account id", ErrProviderCostRevisionInvalid)
	}
	if out.HeadKey == "" {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: head key is required", ErrProviderCostRevisionInvalid)
	}
	if err := economics.ValidateSafeRef("provider cost head key", out.HeadKey); err != nil || strings.TrimSpace(out.HeadKey) != out.HeadKey {
		if err == nil {
			err = errors.New("head key must not have surrounding whitespace")
		}
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: head key: %v", ErrProviderCostRevisionInvalid, err)
	}
	if out.EvidenceRevision == 0 {
		out.EvidenceRevision = out.Revision
	}
	if out.Revision == 0 {
		out.Revision = out.EvidenceRevision
	}
	if out.EvidenceRevision == 0 || out.Revision != out.EvidenceRevision {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: positive revision is required", ErrProviderCostRevisionInvalid)
	}
	if err := normalizeProviderCostResult(&out); err != nil {
		return ProviderCostRevisionInput{}, err
	}
	if err := validateProviderCostRevisionEvidence(out); err != nil {
		return ProviderCostRevisionInput{}, err
	}
	if out.InputSetHash == "" {
		fingerprint, err := out.costFingerprint()
		if err != nil {
			return ProviderCostRevisionInput{}, err
		}
		out.InputSetHash = fingerprint
	}
	if _, err := NewEconomicRevisionIdentity(EconomicQueueProvider, out.HeadKey, out.EvidenceRevision, out.InputSetHash); err != nil {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: input set hash: %v", ErrProviderCostRevisionInvalid, err)
	}
	if out.ValuationID == "" {
		identity, err := NewEconomicRevisionIdentity(EconomicQueueProvider, out.HeadKey, out.EvidenceRevision, out.InputSetHash)
		if err != nil {
			return ProviderCostRevisionInput{}, err
		}
		out.ValuationID = identity.ValuationKey()
	}
	if err := economics.ValidateSafeRef("provider cost valuation id", out.ValuationID); err != nil {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: valuation id: %v", ErrProviderCostRevisionInvalid, err)
	}
	return out, nil
}

// Normalize validates and returns a copy of the provider revision envelope.
func (in ProviderCostRevisionInput) Normalize() (ProviderCostRevisionInput, error) {
	return in.normalized()
}

func normalizeProviderCostResult(in *ProviderCostRevisionInput) error {
	cost := in.Cost
	// These projections are sets/views of the selected-cost reducer. Detach
	// their backing arrays before canonicalization so Normalize honors its
	// caller-owned copy contract and concurrent retries cannot race on caller
	// memory. Stable ordering also keeps equivalent duplicate deliveries on the
	// same operation fingerprint.
	cost.IncludedLegKeys = append([]string(nil), cost.IncludedLegKeys...)
	cost.UnknownLegKeys = append([]string(nil), cost.UnknownLegKeys...)
	cost.ExcludedLegKeys = append([]string(nil), cost.ExcludedLegKeys...)
	cost.PendingCoverage = append([]metering.ChargeCoverageRef(nil), cost.PendingCoverage...)
	cost.AllocatedCostLines = append([]AllocatedCostLine(nil), cost.AllocatedCostLines...)
	cost.PendingAllocations = append([]economics.AllocationRef(nil), cost.PendingAllocations...)
	for i := range cost.AllocatedCostLines {
		line := &cost.AllocatedCostLines[i]
		if line.SourceAmount != nil {
			amount := *line.SourceAmount
			line.SourceAmount = &amount
		}
		if line.SourceQuantity != nil {
			quantity := *line.SourceQuantity
			line.SourceQuantity = &quantity
		}
		if line.RoundedAmount != nil {
			rounded := *line.RoundedAmount
			line.RoundedAmount = &rounded
		}
	}
	sortAndUniqueStrings(&cost.IncludedLegKeys)
	sortAndUniqueStrings(&cost.UnknownLegKeys)
	sortAndUniqueStrings(&cost.ExcludedLegKeys)
	sort.Slice(cost.PendingCoverage, func(i, j int) bool {
		left, right := cost.PendingCoverage[i], cost.PendingCoverage[j]
		if left.Ref.StoreID != right.Ref.StoreID {
			return left.Ref.StoreID < right.Ref.StoreID
		}
		if left.Ref.ObservationID != right.Ref.ObservationID {
			return left.Ref.ObservationID < right.Ref.ObservationID
		}
		if left.Ref.Revision != right.Ref.Revision {
			return left.Ref.Revision < right.Ref.Revision
		}
		if left.Ref.ChargeItemID != right.Ref.ChargeItemID {
			return left.Ref.ChargeItemID < right.Ref.ChargeItemID
		}
		return left.Relation < right.Relation
	})
	cost.PendingCoverage = uniqueChargeCoverageRefs(cost.PendingCoverage)
	sort.Slice(cost.AllocatedCostLines, func(i, j int) bool {
		left, right := cost.AllocatedCostLines[i], cost.AllocatedCostLines[j]
		if left.AllocationID != right.AllocationID {
			return left.AllocationID < right.AllocationID
		}
		if left.AllocationVersion != right.AllocationVersion {
			return left.AllocationVersion < right.AllocationVersion
		}
		if left.AllocationRevision != right.AllocationRevision {
			return left.AllocationRevision < right.AllocationRevision
		}
		return left.TargetID < right.TargetID
	})
	sort.Slice(cost.PendingAllocations, func(i, j int) bool {
		left, right := cost.PendingAllocations[i], cost.PendingAllocations[j]
		if left.StoreID != right.StoreID {
			return left.StoreID < right.StoreID
		}
		if left.AllocationID != right.AllocationID {
			return left.AllocationID < right.AllocationID
		}
		if left.Version != right.Version {
			return left.Version < right.Version
		}
		return left.PayloadHash < right.PayloadHash
	})
	cost.PendingAllocations = uniqueAllocationRefs(cost.PendingAllocations)
	// Inputs may be retried concurrently by independent workers. Detach the
	// currency map before normalization adds/consolidates the convenience
	// subtotal; callers must never observe a concurrent map write.
	if cost.KnownSubtotalByCurrency != nil {
		cloned := make(map[string]Money, len(cost.KnownSubtotalByCurrency))
		maps.Copy(cloned, cost.KnownSubtotalByCurrency)
		cost.KnownSubtotalByCurrency = cloned
	}
	if in.AmountPresent {
		if err := in.Amount.Validate(); err != nil {
			return fmt.Errorf("%w: amount: %v", ErrProviderCostRevisionInvalid, err)
		}
		if in.Amount.Nano < 0 {
			return fmt.Errorf("%w: amount cannot be negative", ErrProviderCostRevisionInvalid)
		}
		if cost.KnownSubtotalByCurrency == nil {
			cost.KnownSubtotalByCurrency = make(map[string]Money)
		}
		cost.KnownSubtotalByCurrency[in.Amount.Currency] = in.Amount
		if cost.Completeness == "" {
			cost.Completeness = CostCompletenessKnown
		}
		if !cost.Payable {
			cost.Payable = in.Authoritative
		}
	}
	if cost.KnownSubtotalByCurrency == nil {
		cost.KnownSubtotalByCurrency = make(map[string]Money)
	}
	if cost.KnownSubtotal.Currency != "" {
		if prior, ok := cost.KnownSubtotalByCurrency[cost.KnownSubtotal.Currency]; ok && prior.Nano != cost.KnownSubtotal.Nano {
			return fmt.Errorf("%w: selected subtotal conflicts with currency map", ErrProviderCostRevisionInvalid)
		}
		cost.KnownSubtotalByCurrency[cost.KnownSubtotal.Currency] = cost.KnownSubtotal
	}
	for currency, amount := range cost.KnownSubtotalByCurrency {
		if strings.TrimSpace(currency) == "" || amount.Currency != currency {
			return fmt.Errorf("%w: selected cost currency mismatch", ErrProviderCostRevisionInvalid)
		}
		if err := amount.Validate(); err != nil {
			return fmt.Errorf("%w: selected cost: %v", ErrProviderCostRevisionInvalid, err)
		}
		if amount.Nano < 0 {
			return fmt.Errorf("%w: selected cost cannot be negative", ErrProviderCostRevisionInvalid)
		}
	}
	if cost.Completeness == "" {
		cost.Completeness = CostCompletenessKnown
	}
	if cost.Completeness != CostCompletenessKnown && cost.Completeness != CostCompletenessPartial {
		return fmt.Errorf("%w: unknown cost completeness %q", ErrProviderCostRevisionInvalid, cost.Completeness)
	}
	if cost.Completeness == CostCompletenessPartial {
		cost.Payable = false
	}
	if len(cost.KnownSubtotalByCurrency) == 0 {
		cost.Payable = false
	}
	// Recompute the convenience subtotal only when exactly one native currency
	// is present. Store adapters choose their account currency explicitly.
	if len(cost.KnownSubtotalByCurrency) == 1 {
		for _, amount := range cost.KnownSubtotalByCurrency {
			cost.KnownSubtotal = amount
		}
	} else {
		cost.KnownSubtotal = Money{}
	}
	in.Cost = cost
	return nil
}

func validateProviderCostRevisionEvidence(in ProviderCostRevisionInput) error {
	evidencePresent := providerCostEvidencePresent(in.Evidence)
	if !in.Cost.Payable {
		if !evidencePresent {
			if in.Authoritative {
				return providerCostRevisionAuthorityError("authoritative", "true", "trusted provider evidence")
			}
			// A complete payer exclusion and an incomplete/partial coverage result
			// are both valid non-posting outcomes. They do not assert monetary
			// authority and therefore do not need an observed charge payload.
			return nil
		}
	}
	if in.Cost.Payable && !in.Authoritative {
		return providerCostRevisionAuthorityError("authoritative", "false", "true for payable provider COGS")
	}
	if !evidencePresent {
		return providerCostRevisionAuthorityError("evidence", "", "observed provider charge evidence")
	}

	evidence := in.Evidence
	if err := validateProviderEvidenceEnvelope(evidence, in.Subject, in.Cost.Payable, in.Cost.Payable); err != nil {
		return err
	}
	if !in.Cost.Payable {
		return nil
	}

	derived := OperatorCOGSResult{
		KnownSubtotalByCurrency: make(map[string]Money),
		Completeness:            CostCompletenessKnown,
		Payable:                 true,
	}
	if err := attributeV2Charges(&derived, providerChargeObservations(evidence.Observations)); err != nil {
		return fmt.Errorf("%w: provider charge selection: %v", ErrProviderCostRevisionInvalid, err)
	}
	if !derived.Payable || derived.Completeness != CostCompletenessKnown || len(derived.IncludedLegKeys) == 0 {
		return providerCostRevisionAuthorityError("payer", "non-operator or incomplete", string(metering.PaymentPartyOperator))
	}
	if !sameProviderCostSubtotals(derived.KnownSubtotalByCurrency, in.Cost.KnownSubtotalByCurrency) {
		return providerCostRevisionAuthorityError("cost", "does not match provider charges", "provider-reported subtotal")
	}
	return nil
}

func providerCostEvidencePresent(evidence economics.PostUsageRatingInput) bool {
	return evidence.Version != 0 || evidence.Perspective != "" || evidence.Basis != "" ||
		evidence.Subject.Kind != "" || evidence.Scope != "" || evidence.Payer.Kind != "" ||
		len(evidence.Observations) != 0 || len(evidence.ObservationRefs) != 0 || evidence.InputSetHash != ""
}

func isProviderEvidenceAcquisition(acquisition string) bool {
	switch acquisition {
	case metering.AcquisitionProviderCountAPI, metering.AcquisitionProviderResponse,
		metering.AcquisitionProviderHeader, metering.AcquisitionProviderFinalizer:
		return true
	default:
		return false
	}
}

func providerChargeObservations(observations []metering.Observation) []metering.Observation {
	charges := make([]metering.Observation, 0, len(observations))
	for _, observation := range observations {
		if isProviderChargeObservation(observation) {
			charges = append(charges, observation)
		}
	}
	return charges
}

func sameProviderCostSubtotals(left, right map[string]Money) bool {
	for currency, amount := range left {
		other, ok := right[currency]
		if !ok || amount != other {
			return false
		}
	}
	return true
}

func (in ProviderCostRevisionInput) costFingerprint() (string, error) {
	payload, err := json.Marshal(struct {
		Version  string                         `json:"version"`
		Account  string                         `json:"account"`
		Call     string                         `json:"call"`
		Head     string                         `json:"head"`
		Rev      uint64                         `json:"revision"`
		Subject  metering.SubjectRef            `json:"subject"`
		Cost     OperatorCOGSResult             `json:"cost"`
		Evidence economics.PostUsageRatingInput `json:"evidence"`
	}{"provider-cost-input-hash:v2", in.AccountID, in.CallID.String(), in.HeadKey, in.EvidenceRevision, in.Subject, in.Cost, in.Evidence})
	if err != nil {
		return "", fmt.Errorf("%w: cost identity: %v", ErrProviderCostRevisionInvalid, err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func sortAndUniqueStrings(values *[]string) {
	if values == nil || len(*values) < 2 {
		return
	}
	sort.Strings(*values)
	write := 1
	for _, value := range (*values)[1:] {
		if value == (*values)[write-1] {
			continue
		}
		(*values)[write] = value
		write++
	}
	*values = (*values)[:write]
}

func uniqueChargeCoverageRefs(values []metering.ChargeCoverageRef) []metering.ChargeCoverageRef {
	if len(values) < 2 {
		return values
	}
	write := 1
	for _, value := range values[1:] {
		if value == values[write-1] {
			continue
		}
		values[write] = value
		write++
	}
	return values[:write]
}

func uniqueAllocationRefs(values []economics.AllocationRef) []economics.AllocationRef {
	if len(values) < 2 {
		return values
	}
	write := 1
	for _, value := range values[1:] {
		if value == values[write-1] {
			continue
		}
		values[write] = value
		write++
	}
	return values[:write]
}

// SemanticFingerprint is the immutable operation identity. Payer and
// completeness are included so a previously ignored/unreconciled result can
// never be silently replayed as payable under the same revision.
func (in ProviderCostRevisionInput) SemanticFingerprint() (string, error) {
	normalized, err := in.normalized()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Version      string
		AccountID    string
		CallID       BillingCallID
		Subject      metering.SubjectRef
		HeadKey      string
		Revision     uint64
		InputSetHash string
		ValuationID  string
		Cost         OperatorCOGSResult
		Evidence     economics.PostUsageRatingInput
	}{"provider-cost-revision:v2", normalized.AccountID, normalized.CallID, normalized.Subject, normalized.HeadKey, normalized.EvidenceRevision, normalized.InputSetHash, normalized.ValuationID, normalized.Cost, normalized.Evidence})
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint: %v", ErrProviderCostRevisionInvalid, err)
	}
	digest := sha256.Sum256(payload)
	return "provider-cost-revision:v2:" + hex.EncodeToString(digest[:]), nil
}

// ProviderCostRevisionSourceKey returns a bounded revision-specific source
// identity. The stable head key remains in the preimage, while the durable
// operation/index value is a fixed-size digest.
func ProviderCostRevisionSourceKey(in ProviderCostRevisionInput) (string, error) {
	normalized, err := in.normalized()
	if err != nil {
		return "", err
	}
	return providerRevisionSourceKeyFromComponents(normalized.AccountID, normalized.CallID.String(), normalized.HeadKey, normalized.EvidenceRevision, normalized.InputSetHash)
}

func providerRevisionSourceKeyFromComponents(accountID, callID, headKey string, revision uint64, inputSetHash string) (string, error) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(callID) == "" || strings.TrimSpace(headKey) == "" || revision == 0 || strings.TrimSpace(inputSetHash) == "" {
		return "", fmt.Errorf("%w: revision source identity requires account/call/head/revision/hash", ErrProviderCostRevisionInvalid)
	}
	payload := accountID + "\x00" + callID + "\x00" + headKey + "\x00" + fmt.Sprint(revision) + "\x00" + inputSetHash
	digest := sha256.Sum256([]byte(payload))
	return "provider-cost-revision:v1:" + hex.EncodeToString(digest[:]), nil
}

// ProviderRevisionPostingOperationKey derives the immutable revision-specific
// posting pin/operation identity for one provider monetary revision/outcome.
// It is ScopedOperationKey("provider_call_cogs", account, sourceKey) where
// sourceKey is ProviderCostRevisionSourceKey. Base legacy charges keep their
// own ProviderCostSourceKey lineage operation; each higher/replacement
// revision is a distinct pin while heads/fences still order lineage. Completed
// pins/outcomes are immutable: exact same operation+fingerprint+tx replay only.
func ProviderRevisionPostingOperationKey(in ProviderCostRevisionInput) (string, error) {
	normalized, err := in.normalized()
	if err != nil {
		return "", err
	}
	sourceKey, err := providerRevisionSourceKeyFromComponents(normalized.AccountID, normalized.CallID.String(), normalized.HeadKey, normalized.EvidenceRevision, normalized.InputSetHash)
	if err != nil {
		return "", err
	}
	key := ScopedOperationKey("provider_call_cogs", normalized.AccountID, sourceKey)
	if err := validatePostingOperationKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// ProviderRevisionPostingOperationKeyForWork derives the same revision-specific
// pin for one monetary economic work item before valuation exists. fullHash
// must be the allocation-aware full input hash (DerivationHash when present,
// else InputSetHash) so work-based classification matches the later revision
// input whose InputSetHash prefers the persisted valuation full hash.
func ProviderRevisionPostingOperationKeyForWork(accountID string, callID BillingCallID, headKey string, revision uint64, fullHash string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", fmt.Errorf("%w: %w: account id is required", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	if err := callID.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w: %v", ErrPostingOwnershipInvalid, ErrInvalidRecord, err)
	}
	if strings.TrimSpace(headKey) == "" || revision == 0 || strings.TrimSpace(fullHash) == "" {
		return "", fmt.Errorf("%w: %w: revision identity requires head/revision/hash", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	sourceKey, err := providerRevisionSourceKeyFromComponents(strings.TrimSpace(accountID), callID.String(), strings.TrimSpace(headKey), revision, strings.TrimSpace(fullHash))
	if err != nil {
		return "", err
	}
	key := ScopedOperationKey("provider_call_cogs", strings.TrimSpace(accountID), sourceKey)
	if err := validatePostingOperationKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// IsProviderRevisionPinKey reports whether key is a revision-specific provider
// pin (scoped provider_call_cogs operation derived from ProviderCostRevisionSourceKey).
func IsProviderRevisionPinKey(key string) bool {
	return strings.HasPrefix(key, "provider_call_cogs:v1:")
}

// BuildProviderCostRevisionInput derives an operator-payable selected-cost
// revision from the immutable observations carried by economic work. It uses
// the same coverage/payer reducer as AttributeOperatorCOGS and therefore never
// promotes BYOK/customer-payable or unknown charges into COGS.
func BuildProviderCostRevisionInput(work EconomicRevisionWork, valuation economics.Valuation) (ProviderCostRevisionInput, error) {
	if work.Queue == EconomicQueueProvider {
		if err := validateProviderCostWorkEvidence(work); err != nil {
			return ProviderCostRevisionInput{}, err
		}
	}
	normalized, err := work.Normalize()
	if err != nil {
		return ProviderCostRevisionInput{}, err
	}
	if normalized.Queue != EconomicQueueProvider {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: provider queue required", ErrProviderCostRevisionInvalid)
	}
	if normalized.Subject.Kind != metering.SubjectBLeg && normalized.Subject.Kind != metering.SubjectProviderCharge {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: B-leg/provider-charge subject required", ErrProviderCostRevisionInvalid)
	}
	callRaw := normalized.Subject.BillingCallID
	if callRaw == "" {
		callRaw = normalized.Subject.CallID
	}
	callID, err := ParseBillingCallID(callRaw)
	if err != nil {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: call id: %v", ErrProviderCostRevisionInvalid, err)
	}
	cost := OperatorCOGSResult{KnownSubtotalByCurrency: make(map[string]Money), Completeness: CostCompletenessKnown, Payable: true}
	for i, observation := range normalized.Input.Observations {
		if !providerEconomicObservationInScope(observation, normalized.Subject) {
			return ProviderCostRevisionInput{}, fmt.Errorf("%w: observation %d is outside B-leg scope", ErrProviderCostRevisionInvalid, i)
		}
	}
	if len(normalized.Input.Observations) == 0 {
		cost.Completeness, cost.Payable = CostCompletenessPartial, false
	} else if err := attributeV2Charges(&cost, providerChargeObservations(normalized.Input.Observations)); err != nil {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: select provider charges: %v", ErrProviderCostRevisionInvalid, err)
	}
	// A revision with no operator-owned amount is a known exclusion, not a
	// zero payable posting. This is what makes BYOK/customer payer handling
	// explicit while retaining the immutable valuation result.
	if len(cost.IncludedLegKeys) == 0 {
		cost.Payable = false
	}
	inputSetHash := normalized.InputSetHash
	if valuation.InputSetHash != "" {
		// Prefer the persisted valuation's full allocation-aware identity so an
		// allocation-only correction cannot reuse a stale provider-cost source
		// key keyed by the observation-only hash alone.
		inputSetHash = valuation.InputSetHash
	}
	input := ProviderCostRevisionInput{
		AccountID: normalized.Subject.AccountID, CallID: callID, Subject: normalized.Subject,
		HeadKey: normalized.HeadKey, EvidenceRevision: normalized.EvidenceRevision,
		InputSetHash: inputSetHash, ValuationID: valuation.ID, Cost: cost,
		Authoritative: cost.Payable, Evidence: normalized.Input.Clone(),
	}
	if input.AccountID == "" {
		return ProviderCostRevisionInput{}, fmt.Errorf("%w: subject account id is required", ErrProviderCostRevisionInvalid)
	}
	if input.ValuationID == "" {
		identity, identityErr := normalized.Identity()
		if identityErr != nil {
			return ProviderCostRevisionInput{}, identityErr
		}
		input.ValuationID = identity.ValuationKey()
	}
	return input.normalized()
}

func validateProviderCostWorkEvidence(work EconomicRevisionWork) error {
	return validateProviderEvidenceEnvelope(work.Input, work.Subject, false, false)
}

func validateProviderEvidenceEnvelope(evidence economics.PostUsageRatingInput, subject metering.SubjectRef, requireObserved, requireOperatorPayer bool) error {
	// Check the discriminating trust labels before calling the broad DTO
	// validator. This keeps invalid/invented labels on the typed authority path
	// instead of collapsing them into an unclassified JSON-contract failure.
	if evidence.Basis != economics.BasisProviderReported {
		return providerCostRevisionAuthorityError("basis", string(evidence.Basis), string(economics.BasisProviderReported))
	}
	if evidence.Perspective != metering.PerspectiveOperator {
		return providerCostRevisionAuthorityError("perspective", string(evidence.Perspective), string(metering.PerspectiveOperator))
	}
	switch evidence.Payer.Kind {
	case "", metering.PaymentPartyOperator:
		// An absent payer can remain in a non-payable evidence revision; the
		// charge-level reducer is the source of truth for mixed coverage.
	case metering.PaymentPartyCustomer:
		if requireOperatorPayer {
			return providerCostRevisionAuthorityError("payer", string(evidence.Payer.Kind), string(metering.PaymentPartyOperator))
		}
	case metering.PaymentPartyUnknown, metering.PaymentPartyUnallocated:
		if requireOperatorPayer {
			return providerCostRevisionAuthorityError("payer", string(evidence.Payer.Kind), string(metering.PaymentPartyOperator))
		}
	default:
		return providerCostRevisionAuthorityError("payer", string(evidence.Payer.Kind), "operator or customer")
	}
	if evidence.Subject != subject {
		return providerCostRevisionAuthorityError("subject", "mismatch", "revision B-leg subject")
	}
	if len(evidence.Observations) == 0 {
		if requireObserved {
			return providerCostRevisionAuthorityError("evidence", "references-only", "observed provider charge evidence")
		}
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("%w: provider evidence: %v", ErrProviderCostRevisionInvalid, err)
		}
		return nil
	}
	for i, observation := range evidence.Observations {
		if err := validateProviderObservationAuthority(observation, subject, i, !requireObserved); err != nil {
			return err
		}
		for _, charge := range observation.Charges {
			switch charge.Payer.Kind {
			case "", metering.PaymentPartyOperator, metering.PaymentPartyCustomer:
				// Empty and customer payers are retained for non-payable or mixed
				// coverage; only an operator payer can support payable COGS.
			case metering.PaymentPartyUnknown, metering.PaymentPartyUnallocated:
				if requireOperatorPayer {
					return providerCostRevisionAuthorityError("payer", string(charge.Payer.Kind), string(metering.PaymentPartyOperator))
				}
			default:
				return providerCostRevisionAuthorityError("payer", string(charge.Payer.Kind), "operator or customer")
			}
			if requireOperatorPayer && charge.Amount != nil && charge.Payer.Kind == "" {
				return providerCostRevisionAuthorityError("payer", "<absent>", string(metering.PaymentPartyOperator))
			}
		}
	}
	if err := evidence.Validate(); err != nil {
		return fmt.Errorf("%w: provider evidence: %v", ErrProviderCostRevisionInvalid, err)
	}
	return nil
}

func validateProviderObservationAuthority(observation metering.Observation, subject metering.SubjectRef, index int, allowUnavailable bool) error {
	if isVerifiedStatementObservation(observation) {
		if observation.Perspective != metering.PerspectiveOperator {
			return providerCostRevisionAuthorityError("perspective", string(observation.Perspective), string(metering.PerspectiveOperator))
		}
		if observation.Boundary != metering.BoundaryBackendIngress && observation.Boundary != metering.BoundaryBackendEgress {
			return providerCostRevisionAuthorityError("boundary", string(observation.Boundary), "backend ingress or egress")
		}
		if observation.Lifecycle != metering.LifecycleBackendAttempt {
			return providerCostRevisionAuthorityError("lifecycle", string(observation.Lifecycle), string(metering.LifecycleBackendAttempt))
		}
		if !providerEconomicObservationInScope(observation, subject) {
			return fmt.Errorf("%w: statement observation %d is outside B-leg scope", ErrProviderCostRevisionInvalid, index)
		}
		return nil
	}
	if observation.Origin != metering.OriginProvider {
		return providerCostRevisionAuthorityError("origin", string(observation.Origin), string(metering.OriginProvider))
	}
	if !isProviderEvidenceAcquisition(observation.Acquisition) {
		return providerCostRevisionAuthorityError("acquisition", observation.Acquisition, "provider response/header/count/finalizer")
	}
	if observation.Authority != metering.AuthorityObservedClaim && (!allowUnavailable || observation.Authority != metering.AuthorityUnavailableClaim) {
		return providerCostRevisionAuthorityError("authority", observation.Authority, string(metering.AuthorityObservedClaim))
	}
	if observation.Perspective != metering.PerspectiveOperator {
		return providerCostRevisionAuthorityError("perspective", string(observation.Perspective), string(metering.PerspectiveOperator))
	}
	if observation.Boundary != metering.BoundaryBackendIngress && observation.Boundary != metering.BoundaryBackendEgress {
		return providerCostRevisionAuthorityError("boundary", string(observation.Boundary), "backend ingress or egress")
	}
	if observation.Lifecycle != metering.LifecycleBackendAttempt {
		return providerCostRevisionAuthorityError("lifecycle", string(observation.Lifecycle), string(metering.LifecycleBackendAttempt))
	}
	if observation.Subject.Kind != metering.SubjectBLeg && observation.Subject.Kind != metering.SubjectProviderCharge {
		return providerCostRevisionAuthorityError("subject_kind", string(observation.Subject.Kind), "B-leg or provider charge")
	}
	if !providerObservationInScope(observation, subject) {
		return fmt.Errorf("%w: observation %d is outside B-leg scope", ErrProviderCostRevisionInvalid, index)
	}
	return nil
}

func providerEconomicObservationInScope(observation metering.Observation, subject metering.SubjectRef) bool {
	if isVerifiedStatementObservation(observation) {
		if subject.Kind != metering.SubjectBLeg {
			return false
		}
		if strings.TrimSpace(observation.Subject.StoreID) != subject.StoreID {
			return false
		}
		if correlationStoreID := strings.TrimSpace(observation.Correlation.StoreID); correlationStoreID != "" && correlationStoreID != subject.StoreID {
			return false
		}
		if observation.Subject.AccountID != "" && observation.Subject.AccountID != subject.AccountID {
			return false
		}
		if blegID := firstNonEmptyBridge(observation.Subject.BLegID, observation.Correlation.BLegID); blegID != subject.BLegID {
			return false
		}
		if aLegID := firstNonEmptyBridge(observation.Subject.ALegID, observation.Correlation.ALegID); aLegID != "" && aLegID != subject.ALegID {
			return false
		}
		wantedCallID := firstNonEmptyBridge(subject.BillingCallID, subject.CallID)
		for _, callID := range []string{
			strings.TrimSpace(observation.Subject.BillingCallID), strings.TrimSpace(observation.Subject.CallID),
			strings.TrimSpace(observation.Correlation.BillingCallID), strings.TrimSpace(observation.Correlation.CallID),
		} {
			if callID != "" && callID != wantedCallID {
				return false
			}
		}
		if accountKey := firstNonEmptyBridge(observation.Subject.ProviderAccountKey, observation.Correlation.ProviderAccountKey); accountKey != "" && accountKey != subject.ProviderAccountKey {
			return false
		}
		for _, pair := range [][2]string{
			{firstNonEmptyBridge(observation.Subject.ProviderRequestID, observation.Correlation.ProviderRequestID), subject.ProviderRequestID},
			{firstNonEmptyBridge(observation.Subject.ProviderChargeID, observation.Correlation.ProviderChargeID), subject.ProviderChargeID},
		} {
			if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
				return false
			}
		}
		return wantedCallID != ""
	}
	return providerObservationInScope(observation, subject)
}

func providerObservationInScope(observation metering.Observation, subject metering.SubjectRef) bool {
	if observation.Subject.Kind != metering.SubjectBLeg && observation.Subject.Kind != metering.SubjectProviderCharge {
		return false
	}
	if subject.Kind == metering.SubjectProviderCharge && observation.Subject.Kind != metering.SubjectProviderCharge {
		return false
	}
	if strings.TrimSpace(observation.Subject.StoreID) != subject.StoreID {
		return false
	}
	if correlationStoreID := strings.TrimSpace(observation.Correlation.StoreID); correlationStoreID != "" && correlationStoreID != subject.StoreID {
		return false
	}
	if observation.Subject.AccountID != "" && observation.Subject.AccountID != subject.AccountID {
		return false
	}
	if observation.Subject.BLegID != subject.BLegID {
		return false
	}
	if correlationBLegID := strings.TrimSpace(observation.Correlation.BLegID); correlationBLegID != "" && correlationBLegID != subject.BLegID {
		return false
	}
	if observation.Subject.ALegID != "" && observation.Subject.ALegID != subject.ALegID {
		return false
	}
	if correlationALegID := strings.TrimSpace(observation.Correlation.ALegID); correlationALegID != "" && correlationALegID != subject.ALegID {
		return false
	}
	if subject.Kind == metering.SubjectProviderCharge {
		providerChargeID := strings.TrimSpace(observation.Correlation.ProviderChargeID)
		if providerChargeID == "" {
			providerChargeID = strings.TrimSpace(observation.Subject.ProviderChargeID)
		}
		if providerChargeID != subject.ProviderChargeID {
			return false
		}
	}
	wantedCallID := strings.TrimSpace(subject.BillingCallID)
	if wantedCallID == "" {
		wantedCallID = strings.TrimSpace(subject.CallID)
	}
	for _, callID := range []string{
		strings.TrimSpace(observation.Subject.BillingCallID),
		strings.TrimSpace(observation.Subject.CallID),
		strings.TrimSpace(observation.Correlation.BillingCallID),
		strings.TrimSpace(observation.Correlation.CallID),
	} {
		if callID != "" && callID != wantedCallID {
			return false
		}
	}
	return wantedCallID != ""
}

// BuildProviderCostRevisionInputFromWork is used on worker restart when the
// immutable pure valuation has already been persisted. Work still carries the
// original observations and revision identity, so no rater call or valuation
// read is needed to retry the provider posting.
func BuildProviderCostRevisionInputFromWork(work EconomicRevisionWork) (ProviderCostRevisionInput, error) {
	normalized, err := work.Normalize()
	if err != nil {
		return ProviderCostRevisionInput{}, err
	}
	identity, err := normalized.Identity()
	if err != nil {
		return ProviderCostRevisionInput{}, err
	}
	inputSetHash := identity.DerivationHash
	if inputSetHash == "" {
		inputSetHash = identity.InputSetHash
	}
	return BuildProviderCostRevisionInput(normalized, economics.Valuation{ID: identity.ValuationKey(), InputSetHash: inputSetHash})
}
