// Package continuationsafety provides pure policy for safe canonical continuation
// construction and hidden recovery instruction rendering for Agent Loop Guard.
//
// Boundary: continuationsafety performs no I/O. It reuses existing
// continuation records/materialization and lineage (pkg/lipsdk/continuation)
// and canonical item types (pkg/lipapi). Runtime leg-opening, admission, and
// persistence remain with the runtime/continuation owners. The policy enforces
// committed-output preservation, tool side-effect non-reexecution, bounds,
// and internal-control provenance without depending on frontend bytes or
// provider SDKs.
package continuationsafety
