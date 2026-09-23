package configsource_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
)

// TestFixedSource_AtomicReplaceWithRecycledInodeIsEligible proves the atomicity
// gate keys on file identity that survives inode-number reuse. A rejected
// candidate is still an atomic rename, so it frees the previously accepted
// file's inode; the following recovery rename can be handed that same inode
// number by the filesystem. That is a different physical file, so it must be
// eligible for publication rather than rejected as an in-place rewrite.
func TestFixedSource_AtomicReplaceWithRecycledInodeIsEligible(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")

	atomicWrite := func(body string) {
		t.Helper()
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}

	atomicWrite("body-a")
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snap1, res1, err := src.ReadStable(ctx, nil)
	if err != nil || res1 != configsource.AtomicEligible {
		t.Fatalf("startup read: res=%q err=%v", res1, err)
	}
	active := &configsource.ActiveSourceVersion{
		HandleIdentity: snap1.HandleIdentity,
		PrivateDigest:  snap1.PrivateDigest,
	}

	// A candidate that fails validation is still renamed atomically, freeing the
	// original inode.
	atomicWrite("::: not yaml :::{{")
	if _, res, err := src.ReadStable(ctx, active); err != nil || res != configsource.AtomicEligible {
		t.Fatalf("failed candidate read: res=%q err=%v", res, err)
	}

	// The recovery rename may be allocated the freed inode number.
	atomicWrite("body-b")
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Only exercise the recycled-inode path when the filesystem actually
	// reallocated the freed inode number. Otherwise the test would pass
	// without proving anything, which is worse than an explicit skip.
	if !os.SameFile(info1, info2) {
		t.Skipf("filesystem did not recycle the freed inode number; ABA condition not reproducible here")
	}

	snap2, res2, err := src.ReadStable(ctx, active)
	if err != nil {
		t.Fatalf("recycled-inode atomic replace rejected: %v", err)
	}
	if res2 != configsource.AtomicEligible {
		t.Fatalf("recycled-inode atomic replace: res=%q want eligible", res2)
	}
	if string(snap2.Bytes) != "body-b" {
		t.Fatalf("recycled-inode bytes=%q want body-b", snap2.Bytes)
	}

	// The security gate is preserved: an in-place rewrite of the accepted inode
	// advances neither identity nor birth time and must stay rejected.
	active2 := &configsource.ActiveSourceVersion{
		HandleIdentity: snap2.HandleIdentity,
		PrivateDigest:  snap2.PrivateDigest,
	}
	if err := os.WriteFile(path, []byte("body-c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.ReadStable(ctx, active2); err == nil {
		t.Fatal("in-place rewrite must be rejected")
	} else if cat, _ := configsource.CategoryOf(err); cat != configsource.CategoryNonAtomicUpdate {
		t.Fatalf("in-place rewrite category=%v want %v", cat, configsource.CategoryNonAtomicUpdate)
	}
}
