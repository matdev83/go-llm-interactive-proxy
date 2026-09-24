package economics

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestAllocationConserve_ExactWeightsSharesAndDeterministicResidual(t *testing.T) {
	t.Parallel()

	record := allocationTestRecord(t, "allocation-exact")
	record.SourceAmount = allocationDecimal(t, "0.000000001")
	record.Targets = []AllocationTarget{
		allocationTarget("b-leg-2", "1", "2"),
		allocationTarget("b-leg-1", "1", "2"),
		{Unallocated: true, TargetID: "unallocated", Weight: AllocationFraction{Numerator: "0", Denominator: "1"}},
	}
	record.RoundingResidualPolicy = AllocationResidualToLastTarget

	got, err := ConserveAllocation(record)
	if err != nil {
		t.Fatalf("ConserveAllocation: %v", err)
	}
	if got.SourceSubject.ResourceID != "cache-shared" || got.SourceSubject.PeriodID != "2026-09" || got.SourceSubject.AccountID != "supplier-account" {
		t.Fatalf("source ownership was not retained: %+v", got.SourceSubject)
	}
	if len(got.Targets) != 3 {
		t.Fatalf("targets = %d, want 3", len(got.Targets))
	}
	if got.Targets[0].Target.BLegID != "b-leg-1" || got.Targets[1].Target.BLegID != "b-leg-2" || !got.Targets[2].Unallocated {
		t.Fatalf("targets were not deterministically ordered: %+v", got.Targets)
	}
	if got.Targets[0].Share != (AllocationFraction{Numerator: "1", Denominator: "2"}) || got.Targets[1].Share != (AllocationFraction{Numerator: "1", Denominator: "2"}) {
		t.Fatalf("exact shares = %+v, want one half each", got.Targets)
	}
	if got.RoundedSourceAmount == nil || got.RoundedSourceAmount.NanoUnits != 1 {
		t.Fatalf("rounded source = %+v, want one nano-unit", got.RoundedSourceAmount)
	}
	if got.Targets[0].RoundedAmount.NanoUnits != 0 || got.Targets[1].RoundedAmount.NanoUnits != 1 {
		t.Fatalf("rounded target residual = %d/%d, want 0/1", got.Targets[0].RoundedAmount.NanoUnits, got.Targets[1].RoundedAmount.NanoUnits)
	}
	if got.RoundingResidualNano != 1 {
		t.Fatalf("rounding residual = %d, want 1", got.RoundingResidualNano)
	}

	reordered := record
	reordered.Targets = []AllocationTarget{
		record.Targets[2], record.Targets[0], record.Targets[1],
	}
	reordered.RoundingResidualPolicy = AllocationResidualToLastTarget
	reorderedCanonical, err := ConserveAllocation(reordered)
	if err != nil {
		t.Fatalf("reordered ConserveAllocation: %v", err)
	}
	if got.Fingerprint() != reorderedCanonical.Fingerprint() {
		t.Fatalf("reordered allocation fingerprint = %q, want %q", reorderedCanonical.Fingerprint(), got.Fingerprint())
	}
}

func TestAllocationConserve_RoundingResidualPolicyIsExplicit(t *testing.T) {
	t.Parallel()

	record := allocationTestRecord(t, "allocation-residual-policy")
	record.SourceAmount = allocationDecimal(t, "0.000000001")
	record.Targets = []AllocationTarget{
		allocationTarget("b-leg-1", "1", "2"),
		allocationTarget("b-leg-2", "1", "2"),
	}
	record.RoundingResidualPolicy = AllocationResidualReject
	if _, err := ConserveAllocation(record); !errors.Is(err, ErrAllocationResidualUnassigned) {
		t.Fatalf("reject residual error = %v, want ErrAllocationResidualUnassigned", err)
	}

	record.RoundingResidualPolicy = AllocationResidualToUnallocated
	record.Targets = append(record.Targets, AllocationTarget{TargetID: "unallocated", Unallocated: true, Weight: AllocationFraction{Numerator: "0", Denominator: "1"}})
	got, err := ConserveAllocation(record)
	if err != nil {
		t.Fatalf("to-unallocated residual: %v", err)
	}
	if got.Targets[2].RoundedAmount == nil || got.Targets[2].RoundedAmount.NanoUnits != 1 {
		t.Fatalf("unallocated residual = %+v, want one nano-unit", got.Targets[2].RoundedAmount)
	}
}

func TestAllocationConserve_RejectsNonConservedAndConflictingTargets(t *testing.T) {
	t.Parallel()

	withoutRemainder := allocationTestRecord(t, "allocation-missing-remainder")
	withoutRemainder.Targets = []AllocationTarget{allocationTarget("b-leg-1", "3", "5")}
	if _, err := ConserveAllocation(withoutRemainder); !errors.Is(err, ErrAllocationNotConserved) {
		t.Fatalf("missing unallocated remainder error = %v, want ErrAllocationNotConserved", err)
	}

	overallocated := allocationTestRecord(t, "allocation-over")
	overallocated.Targets = []AllocationTarget{
		allocationTarget("b-leg-1", "3", "5"),
		allocationTarget("b-leg-2", "3", "5"),
	}
	if _, err := ConserveAllocation(overallocated); !errors.Is(err, ErrAllocationNotConserved) {
		t.Fatalf("overallocated error = %v, want ErrAllocationNotConserved", err)
	}

	duplicate := allocationTestRecord(t, "allocation-duplicate")
	duplicate.Targets = []AllocationTarget{
		allocationTarget("b-leg-1", "1", "2"),
		allocationTarget("b-leg-1", "1", "2"),
	}
	if _, err := ConserveAllocation(duplicate); !errors.Is(err, ErrAllocationTargetConflict) {
		t.Fatalf("duplicate target error = %v, want ErrAllocationTargetConflict", err)
	}
}

func TestAllocationConserve_RejectsScopeAndPlaneMismatch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*AllocationRecord)
		want   error
	}{
		{
			name: "cross store target",
			mutate: func(r *AllocationRecord) {
				target := allocationTarget("b-leg-foreign", "1", "1")
				target.Target.StoreID = "other-store"
				r.Targets = []AllocationTarget{target}
			},
			want: ErrAllocationScopeMismatch,
		},
		{
			name: "cross currency target",
			mutate: func(r *AllocationRecord) {
				target := allocationTarget("b-leg-currency", "1", "1")
				target.Currency = "EUR"
				r.Targets = []AllocationTarget{target}
			},
			want: ErrAllocationScopeMismatch,
		},
		{
			name: "cross unit target",
			mutate: func(r *AllocationRecord) {
				r.SourceAmount = nil
				r.SourceQuantity = allocationDecimal(t, "10")
				r.Currency = ""
				r.Unit = metering.UnitSecond
				target := allocationTarget("b-leg-unit", "1", "1")
				target.Unit = metering.UnitToken
				r.Targets = []AllocationTarget{target}
			},
			want: ErrAllocationScopeMismatch,
		},
		{
			name: "cross period target",
			mutate: func(r *AllocationRecord) {
				target := allocationTarget("b-leg-period", "1", "1")
				target.PeriodID = "2026-10"
				r.Targets = []AllocationTarget{target}
			},
			want: ErrAllocationScopeMismatch,
		},
		{
			name: "cross account target",
			mutate: func(r *AllocationRecord) {
				target := allocationTarget("b-leg-account", "1", "1")
				target.AccountID = "other-account"
				r.Targets = []AllocationTarget{target}
			},
			want: ErrAllocationScopeMismatch,
		},
		{
			name: "account gauge cannot become source cost",
			mutate: func(r *AllocationRecord) {
				r.SourceSubject = metering.SubjectRef{
					Kind: metering.SubjectAccountWindow, StoreID: "store-11",
					ProviderAccountKey: "supplier-account", PoolID: "pool", WindowID: "window",
					ResetAt: time.Unix(100, 0).UTC(),
				}
			},
			want: ErrAllocationInvalidSource,
		},
		{
			name: "provider unit debit cannot become money",
			mutate: func(r *AllocationRecord) {
				r.SourceBasis = BasisProviderUnitDebit
			},
			want: ErrAllocationInvalidSource,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := allocationTestRecord(t, tc.name)
			tc.mutate(&record)
			if _, err := ConserveAllocation(record); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAllocationConserve_SignedSemanticsAreTyped(t *testing.T) {
	t.Parallel()

	negative := allocationTestRecord(t, "allocation-negative")
	negative.SourceAmount = allocationDecimal(t, "-1")
	negative.Targets = []AllocationTarget{allocationTarget("b-leg-1", "1", "1")}
	if _, err := ConserveAllocation(negative); !errors.Is(err, ErrAllocationSignMismatch) {
		t.Fatalf("negative allocate error = %v, want ErrAllocationSignMismatch", err)
	}

	refund := negative
	refund.Operation = AllocationOperationRefund
	if _, err := ConserveAllocation(refund); err != nil {
		t.Fatalf("typed refund: %v", err)
	}

	zero := allocationTestRecord(t, "allocation-zero")
	zero.SourceAmount = allocationDecimal(t, "0")
	zero.Operation = AllocationOperationZero
	zero.Targets = []AllocationTarget{allocationTarget("b-leg-1", "1", "1")}
	if _, err := ConserveAllocation(zero); err != nil {
		t.Fatalf("typed zero allocation: %v", err)
	}

	correction := allocationTestRecord(t, "allocation-correction")
	correction.Operation = AllocationOperationCorrection
	correction.SourceAmount = allocationDecimal(t, "-1")
	if _, err := ConserveAllocation(correction); !errors.Is(err, ErrAllocationRevisionRequired) {
		t.Fatalf("correction without supersession error = %v, want ErrAllocationRevisionRequired", err)
	}
	correction.Supersedes = []AllocationRef{{StoreID: "store-11", AllocationID: "allocation-old", Version: 1, PayloadHash: strings.Repeat("a", 64)}}
	if _, err := ConserveAllocation(correction); err != nil {
		t.Fatalf("typed correction: %v", err)
	}
}

func TestAllocationConserve_PreservesExactNonMonetaryQuantity(t *testing.T) {
	t.Parallel()

	record := allocationTestRecord(t, "allocation-quantity")
	record.SourceBasis = BasisProviderQuantityLocal
	record.SourceAmount = nil
	record.SourceQuantity = allocationDecimal(t, "10.25")
	record.Currency = ""
	record.Unit = metering.UnitSecond
	record.Targets = []AllocationTarget{allocationTarget("b-leg-quantity", "1", "1")}
	got, err := ConserveAllocation(record)
	if err != nil {
		t.Fatalf("quantity allocation: %v", err)
	}
	if got.SourceQuantity == nil || got.SourceQuantity.CanonicalString() != "1025/2" || got.RoundedSourceAmount != nil || got.Targets[0].RoundedAmount != nil {
		t.Fatalf("quantity allocation = %+v, want exact quantity without monetary rounding", got)
	}
}

func TestAllocationCanonical_ImmutableVersionAndReplayIdentity(t *testing.T) {
	t.Parallel()

	record := allocationTestRecord(t, "allocation-versioned")
	canonical, err := record.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if canonical.IdentityKey() == "" || canonical.Fingerprint() == "" {
		t.Fatal("canonical allocation has empty identity")
	}

	revision := canonical
	revision.Version = 2
	revision.Supersedes = []AllocationRef{{StoreID: "store-11", AllocationID: canonical.ID, Version: canonical.Version, PayloadHash: canonical.Fingerprint()}}
	revision.Policy.Version = "v2"
	revision.Policy.Hash = strings.Repeat("b", 64)
	if _, err := revision.Canonical(); err != nil {
		t.Fatalf("new immutable allocation version: %v", err)
	}
	if revision.IdentityKey() == canonical.IdentityKey() {
		t.Fatal("new allocation version reused prior identity")
	}
}

func TestAllocationSupersession_UnknownPredecessorIsTypedPending(t *testing.T) {
	t.Parallel()

	record := allocationTestRecord(t, "allocation-pending")
	record.Version = 2
	record.Operation = AllocationOperationCorrection
	record.Supersedes = []AllocationRef{{
		StoreID: "store-11", AllocationID: "allocation-future", Version: 1,
		PayloadHash: strings.Repeat("d", 64),
	}}
	result, err := ResolveAllocationSupersession([]AllocationRecord{record})
	if err != nil {
		t.Fatalf("resolve pending allocation: %v", err)
	}
	if result.Status != AllocationSupersessionPending {
		t.Fatalf("status = %q, want pending", result.Status)
	}
	if len(result.Pending) != 1 || result.Pending[0] != record.Supersedes[0] {
		t.Fatalf("pending = %+v, want %+v", result.Pending, record.Supersedes)
	}
	if len(result.Effective) != 0 {
		t.Fatalf("effective = %d, want pending allocation excluded", len(result.Effective))
	}
	if result.Payable {
		t.Fatal("pending allocation must not be payable")
	}
}

func TestAllocationSupersession_ValidCorrectionIsOrderIndependent(t *testing.T) {
	t.Parallel()

	base := allocationTestRecord(t, "allocation-base")
	canonicalBase, err := base.Canonical()
	if err != nil {
		t.Fatalf("canonical base: %v", err)
	}
	correction := canonicalBase.Clone()
	correction.Version = 2
	correction.Operation = AllocationOperationReplacement
	correction.Supersedes = []AllocationRef{{
		StoreID: canonicalBase.SourceSubject.StoreID, AllocationID: canonicalBase.ID,
		Version: canonicalBase.Version, PayloadHash: canonicalBase.Fingerprint(),
	}}

	for name, records := range map[string][]AllocationRecord{
		"predecessor-first": {canonicalBase, correction},
		"successor-first":   {correction, canonicalBase},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := ResolveAllocationSupersession(records)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if result.Status != AllocationSupersessionResolved || !result.Complete || !result.Payable {
				t.Fatalf("result status=%q complete=%t payable=%t", result.Status, result.Complete, result.Payable)
			}
			if len(result.Effective) != 1 || result.Effective[0].IdentityKey() != correction.IdentityKey() {
				t.Fatalf("effective = %+v, want correction only", result.Effective)
			}
		})
	}
}

func TestAllocationSupersession_RejectsHashScopeForkAndCycle(t *testing.T) {
	t.Parallel()

	base := allocationTestRecord(t, "allocation-graph-base")
	canonicalBase, err := base.Canonical()
	if err != nil {
		t.Fatalf("canonical base: %v", err)
	}
	ref := AllocationRef{
		StoreID: canonicalBase.SourceSubject.StoreID, AllocationID: canonicalBase.ID,
		Version: canonicalBase.Version, PayloadHash: canonicalBase.Fingerprint(),
	}

	t.Run("hash conflict", func(t *testing.T) {
		t.Parallel()
		correction := canonicalBase.Clone()
		correction.ID = "allocation-hash-correction"
		correction.Operation = AllocationOperationCorrection
		correction.Supersedes = []AllocationRef{{
			StoreID: ref.StoreID, AllocationID: ref.AllocationID, Version: ref.Version,
			PayloadHash: strings.Repeat("e", 64),
		}}
		if _, err := ResolveAllocationSupersession([]AllocationRecord{canonicalBase, correction}); !errors.Is(err, ErrAllocationSupersessionConflict) {
			t.Fatalf("error = %v, want hash conflict", err)
		}
	})

	t.Run("scope mismatch", func(t *testing.T) {
		t.Parallel()
		correction := canonicalBase.Clone()
		correction.ID = "allocation-scope-correction"
		correction.SourceSubject.ResourceID = "another-resource"
		correction.Supersedes = []AllocationRef{ref}
		if _, err := ResolveAllocationSupersession([]AllocationRecord{canonicalBase, correction}); !errors.Is(err, ErrAllocationScopeMismatch) {
			t.Fatalf("error = %v, want scope mismatch", err)
		}
	})

	for name, mutate := range map[string]func(*AllocationRecord){
		"tenant": func(record *AllocationRecord) {
			record.SourceSubject.TenantID = "other-tenant"
			for i := range record.Targets {
				if !record.Targets[i].Unallocated {
					record.Targets[i].Target.TenantID = "other-tenant"
				}
			}
		},
		"account":        func(record *AllocationRecord) { record.SourceSubject.AccountID = "other-account" },
		"period":         func(record *AllocationRecord) { record.SourceSubject.PeriodID = "2026-10" },
		"basis":          func(record *AllocationRecord) { record.SourceBasis = BasisStatementReported },
		"policy":         func(record *AllocationRecord) { record.Policy.Method = "other-method" },
		"policy version": func(record *AllocationRecord) { record.Policy.Version = "v2" },
		"policy hash": func(record *AllocationRecord) {
			record.Policy.Hash = strings.Repeat("b", 64)
		},
		"rounding": func(record *AllocationRecord) {
			record.RoundingPolicy = RoundingTowardZero
		},
		"currency": func(record *AllocationRecord) {
			record.Currency = "EUR"
		},
	} {
		t.Run("incompatible "+name, func(t *testing.T) {
			t.Parallel()
			correction := canonicalBase.Clone()
			correction.ID = "allocation-incompatible-" + name
			correction.Operation = AllocationOperationCorrection
			correction.Supersedes = []AllocationRef{ref}
			mutate(&correction)
			if _, err := ResolveAllocationSupersession([]AllocationRecord{canonicalBase, correction}); !errors.Is(err, ErrAllocationScopeMismatch) {
				t.Fatalf("error = %v, want scope mismatch", err)
			}
		})
	}

	t.Run("unit", func(t *testing.T) {
		t.Parallel()
		baseQuantity := allocationTestRecord(t, "allocation-unit-base")
		baseQuantity.SourceBasis = BasisProviderQuantityLocal
		baseQuantity.SourceAmount = nil
		baseQuantity.SourceQuantity = allocationDecimal(t, "10")
		baseQuantity.Currency = ""
		baseQuantity.Unit = metering.UnitSecond
		canonicalQuantity, err := baseQuantity.Canonical()
		if err != nil {
			t.Fatalf("canonical quantity: %v", err)
		}
		correction := canonicalQuantity.Clone()
		correction.ID = "allocation-incompatible-unit"
		correction.Operation = AllocationOperationCorrection
		correction.Unit = metering.UnitToken
		correction.Supersedes = []AllocationRef{{StoreID: "store-11", AllocationID: canonicalQuantity.ID, Version: 1, PayloadHash: canonicalQuantity.Fingerprint()}}
		if _, err := ResolveAllocationSupersession([]AllocationRecord{canonicalQuantity, correction}); !errors.Is(err, ErrAllocationScopeMismatch) {
			t.Fatalf("error = %v, want unit scope mismatch", err)
		}
	})

	t.Run("self reference and duplicate payload", func(t *testing.T) {
		t.Parallel()
		self := canonicalBase.Clone()
		self.ID = "allocation-self"
		self.Operation = AllocationOperationCorrection
		self.Supersedes = []AllocationRef{{StoreID: "store-11", AllocationID: self.ID, Version: self.Version, PayloadHash: strings.Repeat("f", 64)}}
		if _, err := ResolveAllocationSupersession([]AllocationRecord{self}); !errors.Is(err, ErrAllocationRevisionRequired) {
			t.Fatalf("self error = %v, want revision/self error", err)
		}

		conflict := canonicalBase.Clone()
		conflict.Targets[0].Weight = AllocationFraction{Numerator: "1", Denominator: "1"}
		conflict.Targets = conflict.Targets[:1]
		if _, err := ResolveAllocationSupersession([]AllocationRecord{canonicalBase, conflict}); !errors.Is(err, ErrAllocationSupersessionConflict) {
			t.Fatalf("duplicate identity error = %v, want conflict", err)
		}
	})

	t.Run("forked successors", func(t *testing.T) {
		t.Parallel()
		left := canonicalBase.Clone()
		left.ID = "allocation-left"
		left.Operation = AllocationOperationCorrection
		left.Supersedes = []AllocationRef{ref}
		right := canonicalBase.Clone()
		right.ID = "allocation-right"
		right.Operation = AllocationOperationCorrection
		right.Supersedes = []AllocationRef{ref}
		if _, err := ResolveAllocationSupersession([]AllocationRecord{canonicalBase, left, right}); !errors.Is(err, ErrAllocationSupersessionFork) {
			t.Fatalf("error = %v, want fork", err)
		}
	})

	t.Run("pending fork", func(t *testing.T) {
		t.Parallel()
		left := canonicalBase.Clone()
		left.ID = "allocation-pending-left"
		left.Operation = AllocationOperationCorrection
		left.Supersedes = []AllocationRef{{StoreID: "store-11", AllocationID: "allocation-future-head", Version: 1, PayloadHash: strings.Repeat("d", 64)}}
		right := canonicalBase.Clone()
		right.ID = "allocation-pending-right"
		right.Operation = AllocationOperationCorrection
		right.Supersedes = []AllocationRef{{StoreID: "store-11", AllocationID: "allocation-future-head", Version: 1, PayloadHash: strings.Repeat("d", 64)}}
		if _, err := ResolveAllocationSupersession([]AllocationRecord{left, right}); !errors.Is(err, ErrAllocationSupersessionFork) {
			t.Fatalf("error = %v, want pending fork", err)
		}
	})

	t.Run("multiple active heads", func(t *testing.T) {
		t.Parallel()
		second := canonicalBase.Clone()
		second.Version = 2
		if _, err := ResolveAllocationSupersession([]AllocationRecord{canonicalBase, second}); !errors.Is(err, ErrAllocationSupersessionHeadConflict) {
			t.Fatalf("error = %v, want active-head conflict", err)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		t.Parallel()
		left := canonicalBase.Clone()
		left.ID = "allocation-cycle-left"
		left.Operation = AllocationOperationCorrection
		left.Supersedes = []AllocationRef{{StoreID: "store-11", AllocationID: "allocation-cycle-right", Version: 1, PayloadHash: strings.Repeat("f", 64)}}
		right := canonicalBase.Clone()
		right.ID = "allocation-cycle-right"
		right.Operation = AllocationOperationCorrection
		right.Supersedes = []AllocationRef{{StoreID: "store-11", AllocationID: "allocation-cycle-left", Version: 1, PayloadHash: strings.Repeat("f", 64)}}
		if _, err := ResolveAllocationSupersession([]AllocationRecord{left, right}); !errors.Is(err, ErrAllocationSupersessionCycle) {
			t.Fatalf("error = %v, want cycle", err)
		}
	})
}

func TestAllocationSupersession_ShuffledCorrectionReplacementChainConverges(t *testing.T) {
	t.Parallel()

	base, err := allocationTestRecord(t, "allocation-chain").Canonical()
	if err != nil {
		t.Fatalf("canonical base: %v", err)
	}
	correction := base.Clone()
	correction.Version = 2
	correction.Operation = AllocationOperationCorrection
	correction.SourceAmount = allocationDecimal(t, "12")
	correction.Supersedes = []AllocationRef{{
		StoreID: base.SourceSubject.StoreID, AllocationID: base.ID,
		Version: base.Version, PayloadHash: base.Fingerprint(),
	}}
	correction, err = correction.Canonical()
	if err != nil {
		t.Fatalf("canonical correction: %v", err)
	}
	replacement := correction.Clone()
	replacement.Version = 3
	replacement.Operation = AllocationOperationReplacement
	replacement.SourceAmount = allocationDecimal(t, "13")
	replacement.Supersedes = []AllocationRef{{
		StoreID: correction.SourceSubject.StoreID, AllocationID: correction.ID,
		Version: correction.Version, PayloadHash: correction.Fingerprint(),
	}}
	replacement, err = replacement.Canonical()
	if err != nil {
		t.Fatalf("canonical replacement: %v", err)
	}

	for name, records := range map[string][]AllocationRecord{
		"forward": {base, correction, replacement},
		"reverse": {replacement, correction, base},
		"mixed":   {correction, replacement, base},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := ResolveAllocationSupersession(records)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if result.Status != AllocationSupersessionResolved || !result.Complete || !result.Payable {
				t.Fatalf("result status=%q complete=%t payable=%t", result.Status, result.Complete, result.Payable)
			}
			if len(result.Effective) != 1 || result.Effective[0].Fingerprint() != replacement.Fingerprint() {
				t.Fatalf("effective=%+v, want replacement %s", result.Effective, replacement.IdentityKey())
			}
			if len(result.Superseded) != 2 || len(result.Pending) != 0 {
				t.Fatalf("superseded=%+v pending=%+v, want two superseded refs and no pending", result.Superseded, result.Pending)
			}
		})
	}
}

func TestAllocationSupersession_PendingAncestorTaintsDescendantsUntilResolved(t *testing.T) {
	t.Parallel()

	future, err := allocationTestRecord(t, "allocation-future-ancestor").Canonical()
	if err != nil {
		t.Fatalf("canonical future ancestor: %v", err)
	}
	child := future.Clone()
	child.ID = "allocation-pending-child"
	child.Operation = AllocationOperationCorrection
	child.Supersedes = []AllocationRef{{
		StoreID: future.SourceSubject.StoreID, AllocationID: future.ID,
		Version: future.Version, PayloadHash: future.Fingerprint(),
	}}
	child, err = child.Canonical()
	if err != nil {
		t.Fatalf("canonical pending child: %v", err)
	}
	grandchild := child.Clone()
	grandchild.ID = "allocation-pending-grandchild"
	grandchild.Operation = AllocationOperationReplacement
	grandchild.Supersedes = []AllocationRef{{
		StoreID: child.SourceSubject.StoreID, AllocationID: child.ID,
		Version: child.Version, PayloadHash: child.Fingerprint(),
	}}
	grandchild, err = grandchild.Canonical()
	if err != nil {
		t.Fatalf("canonical pending grandchild: %v", err)
	}

	pending, err := ResolveAllocationSupersession([]AllocationRecord{grandchild, child})
	if err != nil {
		t.Fatalf("resolve pending chain: %v", err)
	}
	if pending.Status != AllocationSupersessionPending || pending.Complete || pending.Payable {
		t.Fatalf("pending chain status=%q complete=%t payable=%t, want pending/incomplete/not payable", pending.Status, pending.Complete, pending.Payable)
	}
	if len(pending.Pending) != 1 || pending.Pending[0].AllocationID != future.ID {
		t.Fatalf("pending chain references=%+v, want unresolved ancestor %q", pending.Pending, future.ID)
	}
	if len(pending.Effective) != 0 {
		t.Fatalf("pending chain effective=%+v, want descendants excluded", pending.Effective)
	}

	resolved, err := ResolveAllocationSupersession([]AllocationRecord{grandchild, future, child})
	if err != nil {
		t.Fatalf("resolve completed chain: %v", err)
	}
	if resolved.Status != AllocationSupersessionResolved || !resolved.Complete || !resolved.Payable {
		t.Fatalf("resolved chain status=%q complete=%t payable=%t", resolved.Status, resolved.Complete, resolved.Payable)
	}
	if len(resolved.Effective) != 1 || resolved.Effective[0].IdentityKey() != grandchild.IdentityKey() {
		t.Fatalf("resolved chain effective=%+v, want grandchild only", resolved.Effective)
	}
}

func allocationTestRecord(t *testing.T, id string) AllocationRecord {
	t.Helper()
	observationHash := strings.Repeat("c", 64)
	return AllocationRecord{
		ID:      id,
		Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: "store-11", TenantID: "tenant-11",
			AccountID: "supplier-account", ResourceID: "cache-shared", PeriodID: "2026-09",
			StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC(),
		},
		SourceBasis:  BasisAllocatedCost,
		SourceAmount: allocationDecimal(t, "10"),
		Currency:     "USD",
		Policy: AllocationPolicyRef{
			Method: "weighted-provider-usage", Version: "v1", Hash: strings.Repeat("a", 64),
		},
		Operation:              AllocationOperationAllocate,
		RoundingScope:          RoundingScopeLine,
		RoundingPolicy:         RoundingHalfEven,
		RoundingResidualPolicy: AllocationResidualToUnallocated,
		SourceObservationRefs: []metering.ObservationRef{{
			StoreID: "store-11", ObservationID: "resource-cost", Revision: 1, PayloadHash: observationHash,
		}},
		Targets: []AllocationTarget{
			allocationTarget("b-leg-1", "3", "5"),
			{Unallocated: true, TargetID: "unallocated", Weight: AllocationFraction{Numerator: "2", Denominator: "5"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
}

func allocationTarget(bLegID, numerator, denominator string) AllocationTarget {
	return AllocationTarget{
		Target: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "store-11", TenantID: "tenant-11",
			ALegID: "a-leg-11", BillingCallID: "billing-call-11", BLegID: bLegID,
		},
		TargetID: bLegID,
		Weight:   AllocationFraction{Numerator: numerator, Denominator: denominator},
	}
}

func allocationDecimal(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("parse decimal %q: %v", raw, err)
	}
	return &value
}
