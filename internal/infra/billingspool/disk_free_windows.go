//go:build windows

package billingspool

import (
	"golang.org/x/sys/windows"
)

func filesystemFreeBytes(path string) (int64, error) {
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(path), &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(free), nil
}
