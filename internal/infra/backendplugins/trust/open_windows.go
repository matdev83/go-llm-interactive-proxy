//go:build windows

package trust

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func openNoFollow(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	attrs := info.FileAttributes
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		return nil, ReasonSymlinkEscape
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = windows.CloseHandle(h)
		return nil, ReasonNotRegular
	}
	final, err := finalPathByHandle(h)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	// Final path must still resolve under the same directory prefix as the
	// requested path (best-effort containment; identity remains the handle).
	if !sameVolumePathPrefix(path, final) {
		_ = windows.CloseHandle(h)
		return nil, ReasonPathEscape
	}
	f := os.NewFile(uintptr(h), path)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, ReasonOpenFailed
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, ReasonNotRegular
	}
	return f, nil
}

func finalPathByHandle(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	if n == 0 || int(n) > len(buf) {
		return "", ReasonOpenFailed
	}
	s := windows.UTF16ToString(buf[:n])
	s = strings.TrimPrefix(s, `\\?\`)
	s = strings.TrimPrefix(s, `\??\`)
	return s, nil
}

func sameVolumePathPrefix(requested, final string) bool {
	req := strings.ToLower(filepathCleanWin(requested))
	fin := strings.ToLower(filepathCleanWin(final))
	if req == fin {
		return true
	}
	// Allow final path when it is the same file after Windows normalization.
	return strings.EqualFold(req, fin)
}

func filepathCleanWin(p string) string {
	p = strings.ReplaceAll(p, `/`, `\`)
	return strings.TrimRight(p, `\`)
}
