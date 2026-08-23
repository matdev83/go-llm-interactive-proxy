// Package stopgate owns the request-level provisional terminal gate for Agent Loop Guard.
//
// Boundary: stopgate composes pure stopguard policy, progress tracking, and
// continuation safety facts to decide whether a terminal candidate releases the
// A-side hold or opens a bounded continuation leg. It performs no I/O beyond
// the injected verifier and owns no billing, routing, or provider semantics.
// All retry/failover and transport recovery stays with existing owners.
package stopgate
