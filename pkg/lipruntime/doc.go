// Package lipruntime is the public production composition facade for LIP.
//
// Closed enterprise modules construct a runtime through [Build] using only
// public packages (requirements 12.1–12.5). The facade delegates to the OSS
// runtimebundle composition root and does not expose Executor internals or
// internal coordinator types.
//
// Explicit whole-config reload and safe status are exposed through
// [Runtime.Reload], [Runtime.ReloadStatus], and [ReloadControl]. Those
// operations delegate to the same runtimehost coordinator/query seams as the
// standard binary and return copied DTOs without paths, secrets, raw YAML,
// mutable config, closers, or runtimebundle internals (requirements 16.1–16.2).
// [Runtime.ExecutorView] is a stable generation-dispatching facade: each
// Execute acquires the current generation and pins the returned stream until
// terminal/close; CancelALeg reaches process-owned cross-generation A-leg
// state (requirements 16.12–16.13).
//
// Enterprise options (metering, evidence, raters, snapshot sources) wire
// descriptor-bound registrations at [Build] time. Executable generations carry
// the evaluator objects used for admission and settlement; metadata-only
// publication is not an enforcement path.
package lipruntime
