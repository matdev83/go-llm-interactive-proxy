package configsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestSourceLeaseOwner_CloseWaitsForBorrowAndCachesFailure(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease, Opaque: [32]byte{1}}
	closeFailure := errors.New("injected close failure")
	var closeCalls atomic.Int32
	owner := newSourceLeaseOwnerWithCloser(file, id, func(*os.File) error {
		closeCalls.Add(1)
		return closeFailure
	})
	version := &ActiveSourceVersion{HandleIdentity: id, leaseCore: owner.core, leaseRequired: true}
	borrow, err := version.Borrow()
	if err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := owner.Close(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with canceled wait context=%v want context canceled", err)
	}
	if got := closeCalls.Load(); got != 0 {
		t.Fatalf("file closed with an outstanding borrow: calls=%d", got)
	}
	if _, err := version.Borrow(); err == nil || SafeReasonOf(err) != "lease_closed" {
		t.Fatalf("new borrow after RequestClose must fail with bounded lease_closed reason: %v", err)
	}

	borrow.Release()
	borrow.Release()
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("final idempotent Release close calls=%d want 1", got)
	}
	first := owner.Close(context.Background())
	second := owner.Close(context.Background())
	if !IsSourceCleanupError(first) || !IsSourceCleanupError(second) || !errors.Is(first, closeFailure) || !errors.Is(second, closeFailure) {
		t.Fatalf("close failure must be cached and marked: first=%v second=%v", first, second)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("cached close calls=%d want 1", got)
	}
}

func TestSourceOwnerSlot_MoveToClearsSourceAndReturnsDisplacedOwner(t *testing.T) {
	t.Parallel()
	openOwner := func(t *testing.T, id byte) *SourceLeaseOwner {
		t.Helper()
		file, err := os.Create(filepath.Join(t.TempDir(), "source.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return newSourceLeaseOwner(file, FileIdentity{Platform: "test", Scheme: "test", Opaque: [32]byte{id}})
	}
	prior := openOwner(t, 1)
	candidate := openOwner(t, 2)
	destination := NewSourceOwnerSlot(prior)
	source := NewSourceOwnerSlot(candidate)
	if displaced := source.MoveTo(destination); displaced != prior {
		t.Fatalf("displaced owner=%p want %p", displaced, prior)
	}
	if source.Take() != nil {
		t.Fatal("source slot retained close authority after MoveTo")
	}
	if owner := destination.Take(); owner != candidate {
		t.Fatalf("destination owner=%p want candidate %p", owner, candidate)
	}
	if err := prior.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceOwnerSlot_MoveToEmptyPreservesDestination(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := newSourceLeaseOwner(file, FileIdentity{Platform: "test", Scheme: "test"})
	destination := NewSourceOwnerSlot(want)
	if displaced := NewSourceOwnerSlot(nil).MoveTo(destination); displaced != nil {
		t.Fatalf("empty source displaced owner %p", displaced)
	}
	if got := destination.Take(); got != want {
		t.Fatalf("empty move changed destination owner: got %p want %p", got, want)
	}
	if err := want.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceBorrowValueCopiesShareReleaseToken(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease}
	var closes atomic.Int32
	owner := newSourceLeaseOwnerWithCloser(file, id, func(f *os.File) error {
		closes.Add(1)
		return f.Close()
	})
	borrow, err := owner.core.borrow(id)
	if err != nil {
		t.Fatal(err)
	}
	independent, err := owner.core.borrow(id)
	if err != nil {
		t.Fatal(err)
	}
	copyA, copyB := *borrow, *borrow
	if err := owner.RequestClose(); err != nil {
		t.Fatal(err)
	}
	copyA.Release()
	copyB.Release()
	if closes.Load() != 0 {
		t.Fatal("releasing borrow copies also released an independent borrow")
	}
	independent.Release()
	if closes.Load() != 1 {
		t.Fatalf("copied Release calls closed file %d times, want once", closes.Load())
	}
	if err := owner.WaitClosed(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyAtomicReplacement_LinuxRequiresLiveLeaseProvenance(t *testing.T) {
	t.Parallel()
	active := ActiveSourceVersion{
		HandleIdentity: FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease, Opaque: [32]byte{1}},
		PrivateDigest:  [32]byte{1},
	}
	candidate := SourceSnapshot{
		HandleIdentity: FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease, Opaque: [32]byte{2}},
		PrivateDigest:  [32]byte{2}, Bytes: []byte("new"),
	}
	result, category, err := ClassifyAtomicReplacement(active, candidate)
	if result != AtomicReject || category != CategoryNonAtomicUpdate || SafeReasonOf(err) != "lease_unavailable" {
		t.Fatalf("unowned Linux identity must reject: result=%q category=%q reason=%q err=%v", result, category, SafeReasonOf(err), err)
	}
}

func TestSourceLeaseOwner_CloseWaitCancellationLeavesReleaseArmed(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "test", Scheme: "test"}
	var closes atomic.Int32
	owner := newSourceLeaseOwnerWithCloser(file, id, func(f *os.File) error {
		closes.Add(1)
		return f.Close()
	})
	borrow, err := owner.core.borrow(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := owner.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error=%v want canceled context", err)
	}
	borrow.Release()
	if closes.Load() != 1 {
		t.Fatalf("close calls after final release=%d want 1", closes.Load())
	}
}

func TestSourceOwnerSlotRejectsInvalidCapabilityPairs(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "source"))
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease, Opaque: [32]byte{1}}
	owner := newSourceLeaseOwner(file, id)
	defer func() { _ = owner.Close(context.Background()) }()
	valid := ActiveSourceVersion{HandleIdentity: id, leaseCore: owner.core, leaseRequired: true}
	for _, tc := range []struct {
		name string
		edit func(*ActiveSourceVersion)
		slot *SourceOwnerSlot
	}{
		{name: "missing", slot: NewSourceOwnerSlot(nil)},
		{name: "consumed", slot: func() *SourceOwnerSlot { s := NewSourceOwnerSlot(owner); s.Take(); return s }()},
		{name: "forged ownerless", slot: NewSourceOwnerSlot(nil), edit: func(v *ActiveSourceVersion) { v.leaseCore = nil; v.leaseRequired = false }},
		{name: "wrong platform", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.HandleIdentity.Platform = "windows" }},
		{name: "wrong scheme", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.HandleIdentity.Scheme = identitySchemeDeviceInode }},
		{name: "identity mismatch", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.HandleIdentity.Opaque[0] = 2 }},
		{name: "mount mismatch", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.mountID = 7 }},
		{name: "device mismatch", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.devMinor = 7 }},
		{name: "unavailable with owner", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.leaseUnavailable = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid
			if tc.edit != nil {
				tc.edit(&v)
			}
			destination := NewSourceOwnerSlot(owner)
			if tc.slot.ValidFor(&v) {
				t.Fatal("invalid capability accepted")
			}
			if _, ok := tc.slot.MoveMatchingTo(destination, &v); ok {
				t.Fatal("invalid capability moved")
			}
			if destination.owner.Load() != owner {
				t.Fatal("rejection displaced active owner")
			}
		})
	}
	slot := NewSourceOwnerSlot(owner)
	if !slot.ValidFor(&valid) {
		t.Fatal("valid supported owner rejected")
	}
	if err := owner.RequestClose(); err != nil {
		t.Fatal(err)
	}
	if slot.ValidFor(&valid) {
		t.Fatal("closed owner accepted")
	}
	if _, ok := slot.MoveMatchingTo(NewSourceOwnerSlot(nil), &valid); ok {
		t.Fatal("closed owner moved")
	}
}

func TestSourceOwnerSlotAllowsOnlyExplicitOwnerlessCapabilities(t *testing.T) {
	t.Parallel()
	for _, v := range []ActiveSourceVersion{
		{HandleIdentity: FileIdentity{Platform: "windows", Scheme: identitySchemeFileID}},
		{HandleIdentity: FileIdentity{Platform: "darwin", Scheme: identitySchemeDeviceInode}},
		{HandleIdentity: FileIdentity{Platform: "linux", Scheme: identitySchemeDeviceInode}, leaseRequired: true, leaseUnavailable: true},
	} {
		if !NewSourceOwnerSlot(nil).ValidFor(&v) {
			t.Fatalf("valid ownerless capability rejected: %+v", v)
		}
	}
	for _, id := range []FileIdentity{
		{Platform: "windows", Scheme: identitySchemeDeviceInode},
		{Platform: "darwin", Scheme: identitySchemeFileID},
		{Platform: "linux", Scheme: identitySchemeDeviceInode},
		{Platform: "test", Scheme: "test"},
	} {
		v := ActiveSourceVersion{HandleIdentity: id}
		if NewSourceOwnerSlot(nil).ValidFor(&v) {
			t.Fatalf("forged ownerless capability accepted: %+v", id)
		}
	}
}
