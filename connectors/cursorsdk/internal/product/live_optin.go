package product

import "strings"

// LiveOptInReady reports whether opt-in live Cursor SDK scenarios may run.
// Default tests and scripts must treat a false result as blocked/skip, never as failure.
func LiveOptInReady(getenv func(string) string) (ready bool, reason string) {
	if getenv == nil {
		return false, "CURSOR_SDK_LIVE=1 not set"
	}
	if getenv("CURSOR_SDK_LIVE") != "1" {
		return false, "CURSOR_SDK_LIVE=1 not set"
	}
	if strings.TrimSpace(getenv("CURSOR_API_KEY")) == "" {
		return false, "CURSOR_API_KEY missing"
	}
	return true, ""
}
