// Package largebody defines the internal provider-neutral DTO contracts for
// the large-payload streaming fast path (#503, work order #532).
//
// The package carries bounded facts only: replay source handles, raw spans,
// body/rewrite contracts, protocol proof, session/recorder inputs, canonical
// semantic identity, assessment and wire facts, rewrite plans, execution
// results, and the sensitive session-response carrier.
//
// Design references (design.md): section 6 (protocol proof and canonical
// semantic identity), section 8 (side-effect-free assessment), section 9
// (backend wire contract), section 10 (secure session and sensitive carrier),
// section 11 (post-commit runtime facts, no shadow Call), section 13
// (ExecutionResult and frontend bridge). Note: design section 4 is the static
// pre-capture disposition, not the DTO contract section.
//
// Hard constraints (Task 2.2, Requirements 4, 6, 7, 8, 9, 14, 16, 18, 22):
// no provider SDK or frontend-specific types (only stdlib plus pkg/lipapi),
// no raw arbitrary header bags, no prompt text, no temp/spool paths, no
// unbounded maps, and no DTO mirroring lipapi.Call.
//
// Boundedness: every Validate method takes the configured semantic-fact
// budget (server.large_payload_fast_path.max_semantic_fact_bytes) and
// rejects oversize strings and over-count slices.
//
// This package holds zero behavior-change plumbing only: types plus pure
// validation. No logic consumes these DTOs yet (later tasks).
package largebody
