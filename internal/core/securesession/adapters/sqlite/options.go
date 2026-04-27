package sqlite

import "time"

// Options configures optional sqlite secure-session store behavior.
// Zero value is valid and matches legacy uncached constructors.
type Options struct {
	SQLQueryCacheTTL        time.Duration
	SQLQueryCacheMaxEntries int
}
