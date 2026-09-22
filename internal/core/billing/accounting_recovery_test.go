package billing

import (
	"errors"
	"testing"
)

// Task 17.4 RED: compatible rollback and recovery policy (Migration Strategy
// step 7, requirements 10.5, 11.6, 17.5, 18.4).
//
// Capture-only rollback is permitted only before any V2 monetary posting is
// durable and while the store compatibility floor remains V1. Once V2
// financial postings exist (or the floor is V2), a serving binary must prove
// V2 reader plus epoch-fencing capability; otherwise startup fails (or the
// operator quiesces strict monetary admissions explicitly). Forward recovery
// with a compatible binary drains durable pending work. These tests must FAIL
// before the recovery contract exists and PASS after, with no globals and no
// SQL.

func recoveryTestMarker(t *testing.T, state AccountingCutoverState) AccountingCutoverMarker {
	t.Helper()
	gen, owner, floor, ok := AccountingCutoverExpectations(state)
	if !ok {
		t.Fatalf("state %q has no expectations", state)
	}
	marker := AccountingCutoverMarker{
		StoreID:            "recovery-17-4",
		Generation:         gen,
		State:              state,
		ActivePostingOwner: owner,
		CompatibilityFloor: floor,
		Version:            4,
		Epoch:              4,
		TransitionID:       "recovery-test",
		CreatedAtUnix:      100,
		UpdatedAtUnix:      100,
	}
	if err := marker.Validate(); err != nil {
		t.Fatalf("test marker invalid: %v", err)
	}
	return marker
}

func recoverySnapshotFor(marker AccountingCutoverMarker, hasV2 bool) AccountingRecoverySnapshot {
	return AccountingRecoverySnapshot{
		StoreID:              marker.StoreID,
		MarkerFound:          true,
		Marker:               marker,
		HasV2MonetaryPosting: hasV2,
	}
}

func TestRecoveryCurrentCapabilitySupportsBothReaders(t *testing.T) {
	capability := CurrentAccountingBinaryCapability()
	if err := capability.Validate(); err != nil {
		t.Fatalf("current capability must validate: %v", err)
	}
	if !capability.SupportsV1Reader || !capability.SupportsV2Reader || !capability.EpochAware {
		t.Fatalf("current capability must be V1+V2 epoch-aware, got %#v", capability)
	}
}

func TestRecoveryCaptureRollbackAllowedBeforeCutover(t *testing.T) {
	capability := CurrentAccountingBinaryCapability()
	stale := AccountingBinaryCapability{SupportsV1Reader: true}
	if err := stale.Validate(); err != nil {
		t.Fatalf("stale V1 capability must validate: %v", err)
	}
	for _, state := range []AccountingCutoverState{
		AccountingCutoverV1Active, AccountingCutoverV2Shadow, AccountingCutoverV1Draining,
	} {
		snapshot := recoverySnapshotFor(recoveryTestMarker(t, state), false)
		if err := CheckCaptureRollbackAllowed(snapshot); err != nil {
			t.Fatalf("state %q without V2 postings must allow capture rollback: %v", state, err)
		}
		if err := CheckAccountingStartup(snapshot, stale); err != nil {
			t.Fatalf("state %q without V2 postings must serve stale binary: %v", state, err)
		}
		if err := CheckAccountingStartup(snapshot, capability); err != nil {
			t.Fatalf("state %q without V2 postings must serve current binary: %v", state, err)
		}
		if AccountingRequiresStrictQuiesce(snapshot, stale) {
			t.Fatalf("state %q without V2 postings must not quiesce", state)
		}
	}
}

func TestRecoveryRollbackBlockedAfterV2Postings(t *testing.T) {
	stale := AccountingBinaryCapability{SupportsV1Reader: true}
	current := CurrentAccountingBinaryCapability()
	for _, state := range []AccountingCutoverState{
		AccountingCutoverV1Active, AccountingCutoverV2Shadow, AccountingCutoverV1Draining, AccountingCutoverV2Active,
	} {
		snapshot := recoverySnapshotFor(recoveryTestMarker(t, state), true)
		if err := CheckCaptureRollbackAllowed(snapshot); !errors.Is(err, ErrAccountingRollbackBlocked) {
			t.Fatalf("state %q with V2 postings must block rollback, got %v", state, err)
		}
		if err := CheckAccountingStartup(snapshot, stale); !errors.Is(err, ErrAccountingStaleBinary) {
			t.Fatalf("state %q with V2 postings must reject stale binary, got %v", state, err)
		}
		if !AccountingRequiresStrictQuiesce(snapshot, stale) {
			t.Fatalf("state %q with V2 postings must quiesce stale binary", state)
		}
		if err := CheckAccountingStartup(snapshot, current); err != nil {
			t.Fatalf("state %q with V2 postings must serve current binary: %v", state, err)
		}
		if AccountingRequiresStrictQuiesce(snapshot, current) {
			t.Fatalf("state %q with V2 postings must not quiesce current binary", state)
		}
	}
}

func TestRecoveryStaleBinaryRejectedOnV2FloorWithoutPostings(t *testing.T) {
	stale := AccountingBinaryCapability{SupportsV1Reader: true}
	current := CurrentAccountingBinaryCapability()
	snapshot := recoverySnapshotFor(recoveryTestMarker(t, AccountingCutoverV2Active), false)
	if err := CheckCaptureRollbackAllowed(snapshot); !errors.Is(err, ErrAccountingRollbackBlocked) {
		t.Fatalf("v2_active floor must block capture rollback even without postings yet, got %v", err)
	}
	if err := CheckAccountingStartup(snapshot, stale); !errors.Is(err, ErrAccountingStaleBinary) {
		t.Fatalf("v2_active floor must reject stale binary, got %v", err)
	}
	if !AccountingRequiresStrictQuiesce(snapshot, stale) {
		t.Fatalf("v2_active floor must quiesce stale binary")
	}
	if err := CheckAccountingStartup(snapshot, current); err != nil {
		t.Fatalf("v2_active floor must serve current binary: %v", err)
	}
}

func TestRecoveryV2CapableWithoutEpochFencingRejected(t *testing.T) {
	// A binary that reads V2 formats but does not understand marker
	// version/epoch claim fencing cannot safely serve V2 financial state:
	// its workers could double-post across an epoch change.
	half := AccountingBinaryCapability{SupportsV1Reader: true, SupportsV2Reader: true}
	if err := half.Validate(); err != nil {
		t.Fatalf("V2-without-epoch capability must validate structurally: %v", err)
	}
	snapshot := recoverySnapshotFor(recoveryTestMarker(t, AccountingCutoverV2Active), true)
	if err := CheckAccountingStartup(snapshot, half); !errors.Is(err, ErrAccountingStaleBinary) {
		t.Fatalf("V2 reader without epoch fencing must be rejected on V2 state, got %v", err)
	}
	if !AccountingRequiresStrictQuiesce(snapshot, half) {
		t.Fatalf("V2 reader without epoch fencing must quiesce on V2 state")
	}
}

func TestRecoveryLegacyStoreWithoutMarkerServesV1Reader(t *testing.T) {
	stale := AccountingBinaryCapability{SupportsV1Reader: true}
	current := CurrentAccountingBinaryCapability()
	snapshot := AccountingRecoverySnapshot{StoreID: "legacy-no-marker", MarkerFound: false}
	if err := CheckCaptureRollbackAllowed(snapshot); err != nil {
		t.Fatalf("legacy store without marker or postings must allow capture rollback: %v", err)
	}
	if err := CheckAccountingStartup(snapshot, stale); err != nil {
		t.Fatalf("legacy store must serve stale V1 binary: %v", err)
	}
	if err := CheckAccountingStartup(snapshot, current); err != nil {
		t.Fatalf("legacy store must serve current binary: %v", err)
	}
	// A legacy store that somehow carries V2 postings (manual operator state)
	// must fail closed for the stale binary even without a marker.
	posted := snapshot
	posted.HasV2MonetaryPosting = true
	if err := CheckAccountingStartup(posted, stale); !errors.Is(err, ErrAccountingStaleBinary) {
		t.Fatalf("legacy store with V2 postings must reject stale binary, got %v", err)
	}
	if err := CheckAccountingStartup(posted, current); err != nil {
		t.Fatalf("legacy store with V2 postings must serve current binary: %v", err)
	}
}

func TestRecoveryUnknownFloorFailsClosed(t *testing.T) {
	current := CurrentAccountingBinaryCapability()
	marker := recoveryTestMarker(t, AccountingCutoverV2Active)
	marker.CompatibilityFloor = "v99"
	snapshot := AccountingRecoverySnapshot{StoreID: marker.StoreID, MarkerFound: true, Marker: marker}
	if err := snapshot.Validate(); err == nil {
		t.Fatalf("unknown compatibility floor must fail snapshot validation")
	}
	if err := CheckAccountingStartup(snapshot, current); err == nil {
		t.Fatalf("unknown floor must fail startup even for the current binary")
	}
	if !AccountingRequiresStrictQuiesce(snapshot, current) {
		t.Fatalf("unknown floor must quiesce")
	}
	if err := CheckCaptureRollbackAllowed(snapshot); err == nil {
		t.Fatalf("unknown floor must block capture rollback")
	}
}

func TestRecoveryMalformedInputsFailClosed(t *testing.T) {
	current := CurrentAccountingBinaryCapability()
	bad := AccountingRecoverySnapshot{}
	if err := CheckCaptureRollbackAllowed(bad); err == nil {
		t.Fatalf("empty snapshot must block rollback")
	}
	if err := CheckAccountingStartup(bad, current); err == nil {
		t.Fatalf("empty snapshot must fail startup")
	}
	zero := AccountingBinaryCapability{}
	if err := zero.Validate(); err == nil {
		t.Fatalf("zero capability must be invalid")
	}
	good := recoverySnapshotFor(recoveryTestMarker(t, AccountingCutoverV1Active), false)
	if err := CheckAccountingStartup(good, zero); err == nil {
		t.Fatalf("zero capability must fail startup even on V1 state")
	}
	if !AccountingRequiresStrictQuiesce(good, zero) {
		t.Fatalf("zero capability must quiesce")
	}
}

func TestRecoveryQuiescePredicateMatchesStartupVerdict(t *testing.T) {
	// Determinism contract: quiesce is required exactly when startup fails
	// with the stale-binary error. No silent downgrade is possible because
	// both paths consume the same predicate.
	capabilities := []AccountingBinaryCapability{
		{SupportsV1Reader: true},
		{SupportsV1Reader: true, SupportsV2Reader: true},
		{SupportsV1Reader: true, SupportsV2Reader: true, EpochAware: true},
		CurrentAccountingBinaryCapability(),
	}
	states := []AccountingCutoverState{
		AccountingCutoverV1Active, AccountingCutoverV2Shadow,
		AccountingCutoverV1Draining, AccountingCutoverV2Active,
	}
	for _, state := range states {
		for _, hasV2 := range []bool{false, true} {
			snapshot := recoverySnapshotFor(recoveryTestMarker(t, state), hasV2)
			for _, capability := range capabilities {
				startupErr := CheckAccountingStartup(snapshot, capability)
				quiesce := AccountingRequiresStrictQuiesce(snapshot, capability)
				if errors.Is(startupErr, ErrAccountingStaleBinary) != quiesce {
					t.Fatalf("state %q postings=%v cap=%#v: stale=%v quiesce=%v must agree",
						state, hasV2, capability, startupErr, quiesce)
				}
				if startupErr == nil && quiesce {
					t.Fatalf("state %q postings=%v cap=%#v: startup passed but quiesce required",
						state, hasV2, capability)
				}
			}
		}
	}
	legacy := AccountingRecoverySnapshot{StoreID: "legacy-no-marker", MarkerFound: false}
	for _, capability := range capabilities {
		startupErr := CheckAccountingStartup(legacy, capability)
		quiesce := AccountingRequiresStrictQuiesce(legacy, capability)
		if errors.Is(startupErr, ErrAccountingStaleBinary) != quiesce {
			t.Fatalf("legacy cap=%#v: stale=%v quiesce=%v must agree", capability, startupErr, quiesce)
		}
	}
}
