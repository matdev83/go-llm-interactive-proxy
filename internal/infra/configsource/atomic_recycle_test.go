//go:build linux && configsource_cert

package configsource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const sourceCertCycles = 1000

func TestFixedSource_PinnedAtomicRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeAtomic(t, path, "accepted-0")
	source, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	initial, result, err := source.ReadStable(ctx, nil)
	if err != nil || result != AtomicEligible || initial.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
		t.Fatalf("supported ext4 startup required: result=%q identity=%q err=%v", result, initial.HandleIdentity.Scheme, err)
	}
	active, activeOwner := initial.TakeBaseline()
	certifyAcceptedPin(t, active, activeOwner)
	defer func() {
		if closeErr := activeOwner.Close(context.Background()); closeErr != nil {
			t.Errorf("close final active owner: %v", closeErr)
		}
	}()

	for i := 0; i < sourceCertCycles; i++ {
		certifyAcceptedPin(t, active, activeOwner)
		writeAtomic(t, path, fmt.Sprintf("rejected-%d", i))
		candidate, got, readErr := source.ReadStable(ctx, active)
		if readErr != nil || got != AtomicEligible {
			t.Fatalf("cycle %d rejected candidate read: result=%q err=%v", i, got, readErr)
		}
		if candidate.HandleIdentity == active.HandleIdentity {
			t.Fatalf("cycle %d candidate reused pinned active identity", i)
		}
		candidateVersion, candidateOwner := candidate.TakeBaseline()
		certifyAcceptedPin(t, candidateVersion, candidateOwner)
		if closeErr := candidateOwner.Close(ctx); closeErr != nil {
			t.Fatalf("cycle %d rejected candidate close: %v", i, closeErr)
		}

		writeAtomic(t, path, fmt.Sprintf("recovery-%d", i))
		recovery, got, readErr := source.ReadStable(ctx, active)
		if readErr != nil || got != AtomicEligible {
			t.Fatalf("cycle %d recovery read: result=%q err=%v", i, got, readErr)
		}
		if recovery.HandleIdentity == active.HandleIdentity {
			t.Fatalf("cycle %d recovery reused pinned active identity", i)
		}
		next, nextOwner := recovery.TakeBaseline()
		certifyAcceptedPin(t, next, nextOwner)
		oldOwner := activeOwner
		active, activeOwner = next, nextOwner
		if closeErr := oldOwner.Close(ctx); closeErr != nil {
			t.Fatalf("cycle %d previous active owner close: %v", i, closeErr)
		}
	}
}

func TestFixedSource_PinPreventsAcceptedInodeReuse(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeAtomic(t, path, "accepted")
	source, err := NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	accepted, result, err := source.ReadStable(ctx, nil)
	if err != nil || result != AtomicEligible || accepted.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease {
		t.Fatalf("supported ext4 startup required: result=%q identity=%q err=%v", result, accepted.HandleIdentity.Scheme, err)
	}
	active, owner := accepted.TakeBaseline()
	certifyAcceptedPin(t, active, owner)
	defer func() {
		if closeErr := owner.Close(context.Background()); closeErr != nil {
			t.Errorf("close active owner: %v", closeErr)
		}
	}()

	noop, got, err := source.ReadStable(ctx, active)
	if err != nil || got != AtomicNoop {
		t.Fatalf("unchanged accepted file: result=%q err=%v, want noop", got, err)
	}
	if noop.HandleIdentity != active.HandleIdentity {
		t.Fatal("unchanged source no-op must retain the accepted physical identity")
	}
	noopVersion, noopOwner := noop.TakeBaseline()
	certifyAcceptedPin(t, noopVersion, noopOwner)
	if closeErr := noopOwner.Close(ctx); closeErr != nil {
		t.Fatalf("close no-op snapshot owner: %v", closeErr)
	}

	if err := os.WriteFile(path, []byte("in-place mutation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, got, err := source.ReadStable(ctx, active); got != AtomicReject || err == nil {
		t.Fatalf("in-place edit: result=%q err=%v, want rejected update", got, err)
	} else if category, ok := CategoryOf(err); !ok || category != CategoryNonAtomicUpdate {
		t.Fatalf("in-place edit category=%q ok=%v err=%v, want non-atomic rejection", category, ok, err)
	}
	certifyPinnedIdentity(t, active, owner)

	for i := 0; i < sourceCertCycles; i++ {
		certifyPinnedIdentity(t, active, owner)
		writeAtomic(t, path, fmt.Sprintf("replacement-%d", i))
		candidate, got, readErr := source.ReadStable(ctx, active)
		if readErr != nil || got != AtomicEligible {
			t.Fatalf("cycle %d replacement read: result=%q err=%v", i, got, readErr)
		}
		if candidate.HandleIdentity == active.HandleIdentity {
			t.Fatalf("cycle %d replacement reused accepted identity", i)
		}
		candidateVersion, candidateOwner := candidate.TakeBaseline()
		certifyAcceptedPin(t, candidateVersion, candidateOwner)
		if closeErr := candidateOwner.Close(ctx); closeErr != nil {
			t.Fatalf("cycle %d candidate close: %v", i, closeErr)
		}
	}
}

func certifyAcceptedPin(t *testing.T, version *ActiveSourceVersion, owner *SourceOwnerSlot) {
	t.Helper()
	if version == nil || owner == nil || !owner.Matches(version) {
		t.Fatal("accepted snapshot owner must match its source version")
	}
	borrow, err := version.Borrow()
	if err != nil {
		t.Fatalf("borrow accepted source: %v", err)
	}
	defer borrow.Release()
	if err := borrow.ValidateBaseline(version); err != nil {
		t.Fatalf("fresh held-handle identity/metadata check: %v", err)
	}
}

func certifyPinnedIdentity(t *testing.T, version *ActiveSourceVersion, owner *SourceOwnerSlot) {
	t.Helper()
	if version == nil || owner == nil || !owner.Matches(version) {
		t.Fatal("accepted snapshot owner must match its source version")
	}
	borrow, err := version.Borrow()
	if err != nil {
		t.Fatalf("borrow accepted source: %v", err)
	}
	defer borrow.Release()
	if err := borrow.ValidateProvenance(version); err != nil {
		t.Fatalf("fresh held-handle identity check: %v", err)
	}
}

func writeAtomic(t *testing.T, path, body string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}
