// Package lipruntime is the public production composition facade for LIP.
//
// Closed enterprise modules construct a runtime through [Build] using only
// public packages (requirements 12.1–12.5). The facade delegates to the OSS
// runtimebundle composition root and does not expose Executor internals.
//
// [Runtime.RefreshSnapshots] republishes injectable snapshot sources for new
// admissions without mutating in-flight generation pins (requirements 11.3, 11.6, 11.7).
package lipruntime
