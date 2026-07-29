package credpool

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CooldownFromRetryAfter parses a Retry-After header value (RFC 7231): either
// delta-seconds or an HTTP-date. On success, the returned time is strictly
// after now. Pure: it does not read or mutate credential pool state.
func CooldownFromRetryAfter(value string, now time.Time) (time.Time, bool) {
	s := strings.TrimSpace(value)
	if s == "" {
		return time.Time{}, false
	}
	if secs, err := strconv.Atoi(s); err == nil {
		if secs <= 0 {
			return time.Time{}, false
		}
		until := now.Add(time.Duration(secs) * time.Second)
		if !until.After(now) {
			return time.Time{}, false
		}
		return until, true
	}
	tm, err := http.ParseTime(s)
	if err != nil {
		return time.Time{}, false
	}
	if !tm.After(now) {
		return time.Time{}, false
	}
	return tm, true
}

// CooldownFromRetryAfterOrFallback returns CooldownFromRetryAfter when parsing
// succeeds; otherwise it returns now+fallback (for provider-local conservative cooldowns).
func CooldownFromRetryAfterOrFallback(value string, now time.Time, fallback time.Duration) time.Time {
	if until, ok := CooldownFromRetryAfter(value, now); ok {
		return until
	}
	return now.Add(fallback)
}
