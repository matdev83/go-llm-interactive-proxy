// Package runtimehost hosts the process generation manager, leases, lifecycle
// worker, stable dispatcher, and (later) coordinator surfaces for versioned
// runtime reload.
//
// Task 3.1 provides production Generation/Lease/Pin/Manager with race-safe
// acquire, atomic publish, transferable pins, and retained-generation budgets.
// Task 3.2 adds LifecycleWorker for post-commit quiesce → drain → close.
// Task 3.4 adds GenerationDispatcher, request-context binding/pin transfer, and
// manager shutdown/detach primitives for the initial-generation host.
// ShutdownDetached fans out bounded per-generation retirement workers so a
// pinned generation cannot stall unrelated drained generations.
// Task 4.6 completes retired-generation quiesce, bounded cleanup retry, panic
// isolation, retention-pressure diagnostics, and reverse-order drain/close.
package runtimehost
