//go:build !windows

package cursorsdk

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	procDirExists     bool
	procDirExistsOnce sync.Once
	bootTime          time.Time
	bootTimeOnce      sync.Once
)

func checkProcDirExists() bool {
	procDirExistsOnce.Do(func() {
		info, err := os.Stat("/proc")
		procDirExists = err == nil && info.IsDir()
	})
	return procDirExists
}

func getBootTime() time.Time {
	bootTimeOnce.Do(func() {
		bootTime = readBootTime()
	})
	return bootTime
}

func platformProcessStartTime(pid int) time.Time {
	if !checkProcDirExists() {
		return time.Time{}
	}
	statPath := "/proc/" + strconv.Itoa(pid) + "/stat"
	data, err := os.ReadFile(statPath)
	if err != nil {
		return time.Time{}
	}
	s := string(data)
	lastParen := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ')' {
			lastParen = i
			break
		}
	}
	if lastParen < 0 || lastParen+1 >= len(s) {
		return time.Time{}
	}
	fields := strings.Fields(s[lastParen+1:])
	if len(fields) < 20 {
		return time.Time{}
	}
	starttimeTicks, ok := parseUint(fields[19])
	if !ok {
		return time.Time{}
	}
	bTime := getBootTime()
	if bTime.IsZero() {
		return time.Time{}
	}
	startSecs := float64(starttimeTicks) / 100.0
	return bTime.Add(time.Duration(startSecs * float64(time.Second)))
}

func platformProcessExePath(pid int) string {
	if pid <= 0 {
		return ""
	}
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	return exe
}

func parseUint(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func readBootTime() time.Time {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) > 6 && line[:6] == "btime " {
			secs, ok := parseUint(line[6:])
			if ok {
				return time.Unix(int64(secs), 0)
			}
			break
		}
	}
	return time.Time{}
}
