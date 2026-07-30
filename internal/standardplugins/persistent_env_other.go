//go:build !windows

package standardplugins

// persistentEnvValue is the non-Windows stub: there is no separate persistent
// environment distinct from the process snapshot, so it always returns "" and
// callers fall back to the process environment.
func persistentEnvValue(string) string { return "" }
