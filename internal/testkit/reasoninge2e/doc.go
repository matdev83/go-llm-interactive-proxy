// Package reasoninge2e provides a deterministic client-transcript model and
// backend-request oracle for reasoning-preservation full HTTP E2E phases.
//
// Plans are precomputed from an explicit seed and retention policy. The package
// never uses package-global RNG or time.Now, returns defensive copies, and keeps
// oracle errors content-safe (no reasoning text, signatures, or opaque payloads).
//
// GenerateTranscriptPlan builds immutable matrix TranscriptPlans (seed+mode+turns)
// with independent backend/client RNG streams and forced coverage categories.
//
// ClientEmulator records actual proxy ChatResponse observations against Plan.Observed,
// then materializes the next Chat request from recorded visible/tool structure plus
// policy-specific submitted reasoning. Observed and submitted histories stay independent.
//
// AssistantTurn.Streaming is plan/client metadata; Check / BackendTurnObservation do
// not validate streaming wire shape (HTTP drivers assert Content-Type / SSE framing).
package reasoninge2e
