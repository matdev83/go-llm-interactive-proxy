package billing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ErrAllocationRollupIncomplete classifies the legacy rollup's fail-closed
// compatibility error. Callers that need known lines from a partial result
// must use RollupAllocatedCostsDetailed instead.
var ErrAllocationRollupIncomplete = errors.New("billing: allocation rollup is incomplete")

// AllocationStore is the billing boundary for immutable explicit allocation
// records. Implementations own persistence; this interface carries no account
// balance mutation and no provider request debit.
type AllocationStore interface {
	AppendAllocation(context.Context, economics.AllocationRecord) error
	GetAllocation(context.Context, string, uint64) (economics.AllocationRecord, error)
	ListAllocations(context.Context, economics.AllocationQuery) (economics.AllocationPage, error)
}

// AllocatedCostLine is a source-preserving rollup view. SourceAmount or
// SourceQuantity remains the original exact value and Share remains the exact
// rational; consumers must not treat this view as provider money or request
// inference evidence.
type AllocatedCostLine struct {
	AllocationID       string
	AllocationVersion  uint64
	AllocationRevision uint64
	Operation          economics.AllocationOperation
	// Policy is the immutable allocation policy that produced Weight/Share.
	// Keeping it on every expanded line prevents a payable rollup from losing
	// the policy/version that explains its conserved distribution.
	Policy         economics.AllocationPolicyRef
	TargetID       string
	SourceSubject  metering.SubjectRef
	SourceBasis    economics.ValuationBasis
	SourceAmount   *metering.Decimal
	SourceQuantity *metering.Decimal
	Currency       string
	Unit           string
	Target         metering.SubjectRef
	Unallocated    bool
	Informational  bool
	Weight         economics.AllocationFraction
	Share          economics.AllocationFraction
	RoundedAmount  *economics.AllocationRoundedAmount
	// InferenceEligible is deliberately always false. A target B-leg is useful
	// for an explicit allocation's informational linkage only when a real
	// request exists; allocation alone never creates request evidence.
	InferenceEligible bool
}

// AllocationRollupResult is the fail-closed rollup view. Pending
// supersession references are retained for audit and make the view incomplete
// and non-payable; only resolved active records contribute Lines.
type AllocationRollupResult struct {
	Lines             []AllocatedCostLine
	Status            economics.AllocationSupersessionStatus
	Pending           []economics.AllocationRef
	PendingSupersedes []economics.AllocationRef
	Complete          bool
	Payable           bool
}

// AllocationRollupIncompleteError reports the detailed state that prevented
// the legacy lines-only API from returning a result. Pending ancestry and
// non-payable or incomplete flags are retained for callers that classify the
// compatibility failure with errors.As.
type AllocationRollupIncompleteError struct {
	Status            economics.AllocationSupersessionStatus
	Pending           []economics.AllocationRef
	PendingSupersedes []economics.AllocationRef
	Complete          bool
	Payable           bool
}

func (e *AllocationRollupIncompleteError) Error() string {
	if e == nil {
		return ErrAllocationRollupIncomplete.Error()
	}
	return fmt.Sprintf("%s: status=%q complete=%t payable=%t pending=%d", ErrAllocationRollupIncomplete, e.Status, e.Complete, e.Payable, len(e.Pending))
}

func (e *AllocationRollupIncompleteError) Unwrap() error {
	return ErrAllocationRollupIncomplete
}

// RollupAllocatedCosts expands canonical records into deterministic target
// lines while retaining each source's basis and ownership. Duplicate exact
// replay rows are collapsed; a conflicting immutable identity is rejected.
func RollupAllocatedCosts(records []economics.AllocationRecord) ([]AllocatedCostLine, error) {
	if len(records) == 0 {
		return nil, nil
	}
	result, err := RollupAllocatedCostsDetailed(records)
	if err != nil {
		return nil, err
	}
	return legacyAllocationLines(result)
}

func legacyAllocationLines(result AllocationRollupResult) ([]AllocatedCostLine, error) {
	if !result.Complete || !result.Payable {
		return nil, &AllocationRollupIncompleteError{
			Status:            result.Status,
			Pending:           append([]economics.AllocationRef(nil), result.Pending...),
			PendingSupersedes: append([]economics.AllocationRef(nil), result.PendingSupersedes...),
			Complete:          result.Complete,
			Payable:           result.Payable,
		}
	}
	return result.Lines, nil
}

// RollupAllocatedCostsDetailed resolves immutable supersession links and
// expands only effective active heads. A missing predecessor is not silently
// treated as resolved: it is returned in Pending and excluded from Lines.
func RollupAllocatedCostsDetailed(records []economics.AllocationRecord) (AllocationRollupResult, error) {
	resolved, err := economics.ResolveAllocationSupersession(records)
	if err != nil {
		return AllocationRollupResult{}, fmt.Errorf("billing: resolve allocation supersession: %w", err)
	}
	result := AllocationRollupResult{
		Lines:             make([]AllocatedCostLine, 0),
		Status:            resolved.Status,
		Pending:           append([]economics.AllocationRef(nil), resolved.Pending...),
		PendingSupersedes: append([]economics.AllocationRef(nil), resolved.Pending...),
		Complete:          resolved.Complete,
		Payable:           resolved.Payable,
	}
	for _, record := range resolved.Effective {
		for _, target := range record.Targets {
			line := AllocatedCostLine{
				AllocationID: record.ID, AllocationVersion: record.Version, AllocationRevision: record.Revision,
				Operation: record.Operation,
				Policy:    record.Policy,
				TargetID:  target.TargetID, SourceSubject: record.SourceSubject,
				SourceBasis: record.SourceBasis, Currency: record.Currency, Unit: record.Unit,
				Target: target.Target, Unallocated: target.Unallocated,
				Informational: target.Informational, Weight: target.Weight, Share: target.Share,
				RoundedAmount:     cloneRoundedAllocationAmount(target.RoundedAmount),
				InferenceEligible: false,
			}
			if record.SourceAmount != nil {
				amount := *record.SourceAmount
				line.SourceAmount = &amount
			}
			if record.SourceQuantity != nil {
				quantity := *record.SourceQuantity
				line.SourceQuantity = &quantity
			}
			result.Lines = append(result.Lines, line)
		}
	}
	sort.SliceStable(result.Lines, func(i, j int) bool {
		if result.Lines[i].AllocationID != result.Lines[j].AllocationID {
			return result.Lines[i].AllocationID < result.Lines[j].AllocationID
		}
		if result.Lines[i].AllocationVersion != result.Lines[j].AllocationVersion {
			return result.Lines[i].AllocationVersion < result.Lines[j].AllocationVersion
		}
		return result.Lines[i].TargetID < result.Lines[j].TargetID
	})
	return result, nil
}

// RollupAllocatedCostsWithStatus is an explicit alias for callers that want
// the pending/completeness state together with the target lines.
func RollupAllocatedCostsWithStatus(records []economics.AllocationRecord) (AllocationRollupResult, error) {
	return RollupAllocatedCostsDetailed(records)
}

func cloneRoundedAllocationAmount(amount *economics.AllocationRoundedAmount) *economics.AllocationRoundedAmount {
	if amount == nil {
		return nil
	}
	copy := *amount
	return &copy
}
