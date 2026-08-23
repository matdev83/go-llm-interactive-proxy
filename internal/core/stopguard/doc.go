// Package stopguard owns the pure request-level policy for Agent Loop Guard:
// canonical terminal-cause vocabulary, verifier-verdict normalization, and the
// conservative mapping from classified candidates to bounded recovery actions.
//
// Boundary: stopguard performs no I/O. Backend/auxiliary execution, canonical
// transcript persistence, terminal mutation, retry/failover selection, billing,
// and provider-specific finish interpretation stay with their existing owners.
// Only a high-confidence CONTINUE verdict naming concrete, already-requested
// remaining work authorizes a continuation leg; every error, timeout, unknown,
// or uncertain outcome normalizes to an allowed stop.
package stopguard
