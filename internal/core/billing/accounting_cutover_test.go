package billing

import (
	"errors"
	"testing"
)

// Phase 17.3A RED: durable per-store cutover marker and monotonic state machine.
// States cover V1 active, capture/shadow, V1 draining/quiescing, V2 posting active
// per Migration Strategy step 6. No claim/worker wiring here.

func TestAccountingCutoverStatesAreDistinct(t *testing.T) {
	t.Parallel()
	states := []AccountingCutoverState{
		AccountingCutoverV1Active,
		AccountingCutoverV2Shadow,
		AccountingCutoverV1Draining,
		AccountingCutoverV2Active,
	}
	seen := map[AccountingCutoverState]bool{}
	for _, s := range states {
		if string(s) == "" {
			t.Fatalf("cutover state must not be empty")
		}
		if seen[s] {
			t.Fatalf("duplicate cutover state %q", s)
		}
		seen[s] = true
		if !s.Valid() {
			t.Fatalf("state %q must be valid", s)
		}
	}
	if (AccountingCutoverState("bogus")).Valid() {
		t.Fatalf("bogus state must be invalid")
	}
}

func TestAccountingCutoverExpectationsPinOwnerAndFloor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state      AccountingCutoverState
		generation int
		owner      string
		floor      string
	}{
		{AccountingCutoverV1Active, AccountingCutoverGenerationV1, AccountingPostingOwnerV1, AccountingCompatFloorV1},
		{AccountingCutoverV2Shadow, AccountingCutoverGenerationV1, AccountingPostingOwnerV1, AccountingCompatFloorV1},
		{AccountingCutoverV1Draining, AccountingCutoverGenerationV1, AccountingPostingOwnerV1, AccountingCompatFloorV1},
		{AccountingCutoverV2Active, AccountingCutoverGenerationV2, AccountingPostingOwnerV2, AccountingCompatFloorV2},
	}
	for _, tc := range cases {
		gen, owner, floor, ok := AccountingCutoverExpectations(tc.state)
		if !ok {
			t.Fatalf("state %q must have expectations", tc.state)
		}
		if gen != tc.generation || owner != tc.owner || floor != tc.floor {
			t.Fatalf("state %q = gen %d owner %q floor %q, want gen %d owner %q floor %q",
				tc.state, gen, owner, floor, tc.generation, tc.owner, tc.floor)
		}
	}
	if _, _, _, ok := AccountingCutoverExpectations(AccountingCutoverState("bogus")); ok {
		t.Fatalf("bogus state must have no expectations")
	}
	// Posting owners must reuse the durable writer lineage, not an invented workflow.
	if AccountingPostingOwnerV1 != HistoricalV1WriterVersion {
		t.Fatalf("V1 posting owner %q must equal HistoricalV1WriterVersion %q", AccountingPostingOwnerV1, HistoricalV1WriterVersion)
	}
	if AccountingPostingOwnerV2 != V2WriterVersion {
		t.Fatalf("V2 posting owner %q must equal V2WriterVersion %q", AccountingPostingOwnerV2, V2WriterVersion)
	}
}

func TestAccountingCutoverDefaultPreservesV1(t *testing.T) {
	t.Parallel()
	marker, err := DefaultAccountingCutoverMarker("store-a", 123)
	if err != nil {
		t.Fatalf("DefaultAccountingCutoverMarker: %v", err)
	}
	if marker.State != AccountingCutoverV1Active {
		t.Fatalf("default state = %q, want %q", marker.State, AccountingCutoverV1Active)
	}
	if marker.ActivePostingOwner != AccountingPostingOwnerV1 {
		t.Fatalf("default owner = %q, want V1", marker.ActivePostingOwner)
	}
	if marker.Generation != AccountingCutoverGenerationV1 {
		t.Fatalf("default generation = %d, want 1", marker.Generation)
	}
	if marker.Version != 1 || marker.Epoch != 1 {
		t.Fatalf("default version/epoch = %d/%d, want 1/1", marker.Version, marker.Epoch)
	}
	if err := marker.Validate(); err != nil {
		t.Fatalf("default marker must validate: %v", err)
	}
}

func TestAccountingCutoverMonotonicChain(t *testing.T) {
	t.Parallel()
	current, err := DefaultAccountingCutoverMarker("store-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	chain := []AccountingCutoverState{AccountingCutoverV2Shadow, AccountingCutoverV1Draining, AccountingCutoverV2Active}
	now := int64(200)
	for i, next := range chain {
		req := AccountingCutoverTransition{
			ExpectedVersion: current.Version,
			ExpectedEpoch:   current.Epoch,
			NextState:       next,
			TransitionID:    string(rune('a'+i)) + "-transition",
		}
		nxt, err := ValidateAccountingCutoverTransition(current, req, now)
		if err != nil {
			t.Fatalf("step %d -> %q: %v", i, next, err)
		}
		if nxt.Version != current.Version+1 || nxt.Epoch != current.Epoch+1 {
			t.Fatalf("step %d version/epoch = %d/%d, want %d/%d", i, nxt.Version, nxt.Epoch, current.Version+1, current.Epoch+1)
		}
		if nxt.State != next {
			t.Fatalf("step %d state = %q, want %q", i, nxt.State, next)
		}
		if err := nxt.Validate(); err != nil {
			t.Fatalf("step %d next marker must validate: %v", i, err)
		}
		current = nxt
		now++
	}
}

func TestAccountingCutoverRejectsSkippedBackwardAndInvalid(t *testing.T) {
	t.Parallel()
	base, err := DefaultAccountingCutoverMarker("store-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	// Skipped: v1_active directly to v1_draining or v2_active.
	for _, skipped := range []AccountingCutoverState{AccountingCutoverV1Draining, AccountingCutoverV2Active} {
		req := AccountingCutoverTransition{ExpectedVersion: base.Version, ExpectedEpoch: base.Epoch, NextState: skipped, TransitionID: "skip"}
		if _, err := ValidateAccountingCutoverTransition(base, req, 200); !errors.Is(err, ErrAccountingCutoverInvalid) {
			t.Fatalf("skipped %q err = %v, want ErrAccountingCutoverInvalid", skipped, err)
		}
	}
	// Backward: advance once then go back.
	shadow, err := ValidateAccountingCutoverTransition(base, AccountingCutoverTransition{ExpectedVersion: 1, ExpectedEpoch: 1, NextState: AccountingCutoverV2Shadow, TransitionID: "t1"}, 200)
	if err != nil {
		t.Fatal(err)
	}
	back := AccountingCutoverTransition{ExpectedVersion: shadow.Version, ExpectedEpoch: shadow.Epoch, NextState: AccountingCutoverV1Active, TransitionID: "back"}
	if _, err := ValidateAccountingCutoverTransition(shadow, back, 300); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("backward err = %v, want ErrAccountingCutoverInvalid", err)
	}
	// Invalid state.
	bad := AccountingCutoverTransition{ExpectedVersion: base.Version, ExpectedEpoch: base.Epoch, NextState: AccountingCutoverState("bogus"), TransitionID: "t"}
	if _, err := ValidateAccountingCutoverTransition(base, bad, 200); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("bogus err = %v, want ErrAccountingCutoverInvalid", err)
	}
	// Same-state is not forward progress.
	same := AccountingCutoverTransition{ExpectedVersion: base.Version, ExpectedEpoch: base.Epoch, NextState: base.State, TransitionID: "same"}
	if _, err := ValidateAccountingCutoverTransition(base, same, 200); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("same-state err = %v, want ErrAccountingCutoverInvalid", err)
	}
	// Empty transition identity is not auditable.
	emptyID := AccountingCutoverTransition{ExpectedVersion: base.Version, ExpectedEpoch: base.Epoch, NextState: AccountingCutoverV2Shadow, TransitionID: ""}
	if _, err := ValidateAccountingCutoverTransition(base, emptyID, 200); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("empty transition id err = %v, want ErrAccountingCutoverInvalid", err)
	}
}

func TestAccountingCutoverRejectsStaleCAS(t *testing.T) {
	t.Parallel()
	base, err := DefaultAccountingCutoverMarker("store-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	stale := AccountingCutoverTransition{ExpectedVersion: 999, ExpectedEpoch: 999, NextState: AccountingCutoverV2Shadow, TransitionID: "stale"}
	if _, err := ValidateAccountingCutoverTransition(base, stale, 200); !errors.Is(err, ErrAccountingCutoverFence) {
		t.Fatalf("stale err = %v, want ErrAccountingCutoverFence", err)
	}
	// Epoch-only mismatch is also stale.
	epochStale := AccountingCutoverTransition{ExpectedVersion: base.Version, ExpectedEpoch: base.Epoch + 1, NextState: AccountingCutoverV2Shadow, TransitionID: "stale-epoch"}
	if _, err := ValidateAccountingCutoverTransition(base, epochStale, 200); !errors.Is(err, ErrAccountingCutoverFence) {
		t.Fatalf("epoch stale err = %v, want ErrAccountingCutoverFence", err)
	}
}

func TestAccountingCutoverReplayDetection(t *testing.T) {
	t.Parallel()
	base, err := DefaultAccountingCutoverMarker("store-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	req := AccountingCutoverTransition{ExpectedVersion: 1, ExpectedEpoch: 1, NextState: AccountingCutoverV2Shadow, TransitionID: "replay-id"}
	next, err := ValidateAccountingCutoverTransition(base, req, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !IsAccountingCutoverReplay(next, req) {
		t.Fatalf("exact replay must be detected")
	}
	other := AccountingCutoverTransition{ExpectedVersion: 1, ExpectedEpoch: 1, NextState: AccountingCutoverV2Shadow, TransitionID: "different-id"}
	if IsAccountingCutoverReplay(next, other) {
		t.Fatalf("conflicting transition id must not be replay")
	}
	otherState := AccountingCutoverTransition{ExpectedVersion: 1, ExpectedEpoch: 1, NextState: AccountingCutoverV1Draining, TransitionID: "replay-id"}
	if IsAccountingCutoverReplay(next, otherState) {
		t.Fatalf("conflicting target state must not be replay")
	}
}

func TestAccountingCutoverBounds(t *testing.T) {
	t.Parallel()
	if _, err := DefaultAccountingCutoverMarker("", 100); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("empty store id err = %v, want ErrAccountingCutoverInvalid", err)
	}
	if _, err := DefaultAccountingCutoverMarker("  ", 100); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("blank store id err = %v, want ErrAccountingCutoverInvalid", err)
	}
	marker, err := DefaultAccountingCutoverMarker("store-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	bad := marker
	bad.TransitionID = ""
	if err := bad.Validate(); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("empty transition id err = %v, want ErrAccountingCutoverInvalid", err)
	}
	bad = marker
	bad.Version = 0
	if err := bad.Validate(); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("zero version err = %v, want ErrAccountingCutoverInvalid", err)
	}
	bad = marker
	bad.ActivePostingOwner = "bogus"
	if err := bad.Validate(); !errors.Is(err, ErrAccountingCutoverInvalid) {
		t.Fatalf("bogus owner err = %v, want ErrAccountingCutoverInvalid", err)
	}
}
