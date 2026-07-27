package product

import (
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type processIdentity struct {
	PID        int
	CreateTime time.Time
	ExeKey     string
}

type processInspector struct {
	StartTime func(pid int) time.Time
	ExePath   func(pid int) string
}

func defaultProcessInspector() processInspector {
	return processInspector{
		StartTime: platformProcessStartTime,
		ExePath:   platformProcessExePath,
	}
}

func (ins processInspector) startTime(pid int) time.Time {
	if ins.StartTime == nil {
		return platformProcessStartTime(pid)
	}
	return ins.StartTime(pid)
}

func (ins processInspector) exePath(pid int) string {
	if ins.ExePath == nil {
		return platformProcessExePath(pid)
	}
	return ins.ExePath(pid)
}

func normalizeExeKey(raw string) string {
	candidate := strings.TrimSpace(strings.Trim(raw, `"`))
	if candidate == "" {
		return ""
	}
	resolved, err := filepath.Abs(candidate)
	if err != nil {
		return normalizeCase(candidate)
	}
	return normalizeCase(resolved)
}

func normalizeCase(s string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(s)
	}
	return s
}

func (ins processInspector) capture(proc Process, cmdFirstArg string) processIdentity {
	pid := proc.PID()
	id := processIdentity{PID: pid}
	if pid <= 0 {
		return id
	}
	if ct := ins.startTime(pid); !ct.IsZero() {
		id.CreateTime = ct
	}
	exe := ins.exePath(pid)
	if exe == "" && cmdFirstArg != "" {
		exe = cmdFirstArg
	}
	if exe != "" {
		id.ExeKey = normalizeExeKey(exe)
	}
	return id
}

func (ins processInspector) stillSame(proc Process, identity processIdentity) bool {
	if identity.PID <= 0 || proc == nil {
		return false
	}
	if proc.PID() != identity.PID {
		return false
	}
	if !identity.CreateTime.IsZero() {
		curCT := ins.startTime(identity.PID)
		if !curCT.IsZero() {
			const epsilon = 750 * time.Millisecond
			if absDuration(curCT.Sub(identity.CreateTime)) > epsilon {
				return false
			}
		}
	}
	if identity.ExeKey != "" {
		curExe := normalizeExeKey(ins.exePath(identity.PID))
		if curExe != "" && curExe != identity.ExeKey {
			return false
		}
	}
	return true
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
