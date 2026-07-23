package runtimehost

import (
	"testing"

	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// TestBoundResultIndependentOfMutablePublicEnumeration proves boundResultName
// uses immutable canonical policy, not the exported mutable AllResultCategories
// compatibility enumeration (Task 2.1 / Hermes review). Must not run in parallel.
func TestBoundResultIndependentOfMutablePublicEnumeration(t *testing.T) {
	orig := append([]sdkreload.ResultCategory(nil), sdkreload.AllResultCategories...)
	t.Cleanup(func() {
		sdkreload.AllResultCategories = append([]sdkreload.ResultCategory(nil), orig...)
	})

	declared := []sdkreload.ResultCategory{
		sdkreload.ResultPublished,
		sdkreload.ResultNoop,
		sdkreload.ResultBusy,
		sdkreload.ResultRestartRequired,
		sdkreload.ResultRetentionBlocked,
		sdkreload.ResultInvalid,
		sdkreload.ResultSourceIntegrity,
		sdkreload.ResultCanceled,
		sdkreload.ResultPreparationFailed,
		sdkreload.ResultInternalFailed,
	}

	sdkreload.AllResultCategories = []sdkreload.ResultCategory{"decoy"}

	for _, c := range declared {
		if got := boundResultName(string(c)); got != string(c) {
			t.Fatalf("after public enumeration mutation, boundResultName(%q)=%q want self", c, got)
		}
	}
	if got := boundResultName("decoy"); got != "other" {
		t.Fatalf("injected decoy must not become trusted: got %q want other", got)
	}
	if got := boundResultName(""); got != "other" {
		t.Fatalf("empty boundResultName=%q want other", got)
	}

	// Extra observer labels and stage binding remain unchanged under mutation.
	for _, extra := range []string{"ok", "accepted", "quiesce_failed", "cleanup_failed", "other"} {
		if got := boundResultName(extra); got != extra {
			t.Fatalf("extra result label boundResultName(%q)=%q", extra, got)
		}
	}
	if got := boundStageName("publish"); got != "publish" {
		t.Fatalf("boundStageName(publish)=%q under mutation", got)
	}
	if got := boundStageName("hostile"); got != "other" {
		t.Fatalf("boundStageName(hostile)=%q want other", got)
	}
}
