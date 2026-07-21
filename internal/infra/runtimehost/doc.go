// Package runtimehost hosts the process generation manager, leases, lifecycle
// worker, stable dispatcher, and serialized reload coordinator for versioned
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
// Task 5.1 adds the production Reload Coordinator: fixed-source read/load,
// no-op and restart-required classification, candidate compile/prepare,
// retention admission, atomic publish, rollback, Busy/coalesce, and
// shutdown-safe terminal status — without duplicating compiler or manager logic.
package runtimehost
