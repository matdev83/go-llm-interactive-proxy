// Package toolcall declares the completed-call finalizer SDK seam (ADR 0007 / issue #152).
//
// Core owns exact original event replay and must pass Finalize a defensive copy of
// ArgsJSON; finalizers must treat CompletedCall as immutable input.
//
// Phase 5 adapter contract: when a repair engine / finalizer returns ActionPass (or an
// engine OutcomePass) with nil ArgsJSON, core must replay the exact original buffered
// argument bytes and lifecycle events. Nil means “unchanged originals”, never empty args.
package toolcall
