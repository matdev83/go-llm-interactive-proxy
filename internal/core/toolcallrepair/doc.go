// Package toolcallrepair holds the deterministic native tool-call repair engine
// contract (ADR 0007, issue #152).
//
// Phase 3 provides offline JSON Schema compilation/validation, catalog indexing,
// and name normalization. Phase 4 implements Engine.Repair: append-only JSON
// syntax completion, unique normalized name/property rewrites, and schema-directed
// deterministic inserts/removals with mandatory post-validation.
//
// OutcomePass leaves ArgsJSON nil; callers (Phase 5 finalizer adapters) must replay
// the exact original buffered argument bytes rather than treating nil as empty JSON.
package toolcallrepair
