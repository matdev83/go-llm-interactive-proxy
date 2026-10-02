// Package outbound implements the first of the feature's two outbound passes: the
// candidate attempt transform of design.md "Existing Architecture and Placement"
// step 2, which virtualizes eligible path-bearing tool history on a backend-bound
// candidate before candidate sizing, context eligibility, and token-accounting
// preflight observe it (requirements.md 5.3).
//
// It exists as a subpackage of the lexical core for the same reason the rewriter and
// the schema-inference step do. The core in internal/plugins/features/pathvirtualization
// keeps no repository import at all, and its host-authority guard proves it; a
// contribution that reads pkg/lipapi and pkg/lipsdk/request must therefore live
// beside that core rather than inside it, or the guard has to be weakened.
//
// The package is deliberately thin. It owns exactly three things and delegates
// everything else:
//
//   - it derives the workspace-bound mapping from the authoritative
//     AttemptMeta.Workspace projection, using the core's existing pure
//     DeriveMapping, and never from anything else the attempt metadata carries;
//   - it hands that mapping to the ONE pure canonical outbound rewriter, in the
//     rollout mode the generation was compiled with, so this pass and the later
//     idempotent request-part hook run identical detection code;
//   - it publishes the rewriter's result onto the canonical call and records a
//     bounded, content-free report.
//
// Three properties are load-bearing and each has a test:
//
//   - the pass mutates ONLY the call it was handed. It reads no routing field, no
//     candidate identity, and no backend identity, and it always answers
//     request.AttemptContinue, so it cannot change route identity, model or backend
//     selection, retry/failover authority, or billing authority
//     (requirements.md 5.5, 5.6, 5.7);
//   - an unexpected transformation failure FAILS OPEN to the real path with a
//     bounded reason, because in the outbound direction no virtual alias is ever
//     client-visible: aliases go to the BACKEND, and the real path is the value the
//     backend must be able to reason about. The rewriter already returns the input
//     call on failure, and the pass refuses to publish any value at all on that
//     path, so a partially rewritten candidate cannot escape;
//   - reapplication is a no-op, because the rewriter's publication is a
//     byte splice that only fires on a segment-boundary real-root prefix
//     (requirements.md 2.9). Task 5.2's request-part hook depends on this.
package outbound
