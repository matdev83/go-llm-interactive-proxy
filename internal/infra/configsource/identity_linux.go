//go:build linux

package configsource

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// statxMask requests the metadata used to prove atomic replacement. Birth time
// is included so a recycled inode number cannot masquerade as an in-place
// rewrite of the previously accepted file: an in-place write preserves birth
// time, while a freshly allocated inode (even reusing a number) gets a new one.
const statxMask = unix.STATX_BASIC_STATS | unix.STATX_BTIME

// statxBtimeAvailable reports whether a successful statx call actually filled
// birth time. A successful call that omits STATX_BTIME must not be promoted to
// the stronger scheme, or a coarser fallback identity would look distinct.
func statxBtimeAvailable(stx *unix.Statx_t) bool {
	return stx.Mask&unix.STATX_BTIME != 0
}

func identityFromFile(f *os.File) (FileIdentity, error) {
	if f == nil {
		return FileIdentity{}, integrityErr(CategoryUnsupportedType)
	}
	var stx unix.Statx_t
	if err := unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH, statxMask, &stx); err == nil && statxBtimeAvailable(&stx) {
		return identityFromStatx(&stx), nil
	}
	fi, err := f.Stat()
	if err != nil {
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	return identityFromFileInfo(fi)
}

func identityFromPath(path string) (FileIdentity, error) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, statxMask, &stx); err == nil && statxBtimeAvailable(&stx) {
		return identityFromStatx(&stx), nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileIdentity{}, integrityErr(CategoryUnstable)
		}
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	return identityFromFileInfo(fi)
}

func identityFromStatx(stx *unix.Statx_t) FileIdentity {
	var opaque [32]byte
	h := sha256.New()
	_ = binary.Write(h, binary.LittleEndian, uint64(stx.Dev_major))
	_ = binary.Write(h, binary.LittleEndian, uint64(stx.Dev_minor))
	_ = binary.Write(h, binary.LittleEndian, stx.Ino)
	_ = binary.Write(h, binary.LittleEndian, stx.Btime.Sec)
	_ = binary.Write(h, binary.LittleEndian, uint64(stx.Btime.Nsec))
	copy(opaque[:], h.Sum(nil))
	return FileIdentity{Platform: runtime.GOOS, Scheme: identitySchemeStatxBirthTime, Opaque: opaque}
}
