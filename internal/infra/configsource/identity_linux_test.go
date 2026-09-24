//go:build linux

package configsource

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestStatxIdentity_BirthTimeDiscriminates proves the Linux identity is keyed
// on inode birth time, not just the inode number: same device+inode with a
// different birth time is a different physical file, while identical fields
// collapse to one identity and carry the statx scheme tag.
func TestStatxIdentity_BirthTimeDiscriminates(t *testing.T) {
	t.Parallel()
	base := unix.Statx_t{Dev_major: 1, Dev_minor: 2, Ino: 100, Btime: unix.StatxTimestamp{Sec: 10, Nsec: 1}}
	same := base
	reusedInode := base
	reusedInode.Btime = unix.StatxTimestamp{Sec: 10, Nsec: 2}

	idBase := identityFromStatx(&base)
	idSame := identityFromStatx(&same)
	idReused := identityFromStatx(&reusedInode)

	if idBase != idSame {
		t.Fatal("identical statx fields must produce identical identity")
	}
	if idBase == idReused {
		t.Fatal("differing birth time must produce distinguishable identity")
	}
	if idBase.Scheme != identitySchemeStatxBirthTime {
		t.Fatalf("statx identity scheme=%q want %q", idBase.Scheme, identitySchemeStatxBirthTime)
	}
}
