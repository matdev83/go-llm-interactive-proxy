//go:build unix

package configsource

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

func identityFromFile(f *os.File) (FileIdentity, error) {
	if f == nil {
		return FileIdentity{}, integrityErr(CategoryUnsupportedType)
	}
	fi, err := f.Stat()
	if err != nil {
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	return identityFromFileInfo(fi)
}

func identityFromPath(path string) (FileIdentity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileIdentity{}, integrityErr(CategoryUnstable)
		}
		return FileIdentity{}, fmt.Errorf("configsource: %s: %w", CategoryPartialUnreadable, err)
	}
	return identityFromFileInfo(fi)
}

func identityFromFileInfo(fi os.FileInfo) (FileIdentity, error) {
	if fi == nil {
		return FileIdentity{}, integrityErr(CategoryUnsupportedType)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return FileIdentity{}, fmt.Errorf("configsource: %s", CategoryUnsupportedType)
	}
	var opaque [32]byte
	h := sha256.New()
	_ = binary.Write(h, binary.LittleEndian, uint64(st.Dev))
	_ = binary.Write(h, binary.LittleEndian, uint64(st.Ino))
	copy(opaque[:], h.Sum(nil))
	return FileIdentity{Platform: runtime.GOOS, Opaque: opaque}, nil
}
