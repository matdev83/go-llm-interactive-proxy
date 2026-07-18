// Package lipruntime is the public production composition facade for LIP.
//
// Closed enterprise modules construct a runtime through [Build] using only
// public packages (requirements 12.1–12.5). The facade delegates to the OSS
// runtimebundle composition root and does not expose Executor internals or
// internal coordinator types.
//
// Executable generations carry the evaluator objects used for admission and
// settlement. [Runtime.RefreshSnapshots] refreshes injectable source-fetch
// metadata compatibility views and, on success, republishes an executable
// generation for new admissions without mutating in-flight pins (requirements
// 9.6–9.9, 11.3, 11.6, 11.7). Metadata-only publication is not an enforcement
// path; use descriptor-bound registrations and [Runtime.ExecutableEvidenceObjectID]
// for decision evidence.
package lipruntime
