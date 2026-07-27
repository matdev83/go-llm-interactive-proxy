// Package runtimehost hosts the process generation manager, leases, retirement
// scheduling, stable dispatcher, and serialized reload coordinator for
// versioned runtime reload.
//
// Task 3.1 provides production Generation/Lease/Pin/Manager with race-safe
// acquire, atomic publish, transferable pins, and retained-generation budgets.
// Task 3.2 added post-commit quiesce → drain → close retirement.
// Task 3.4 adds GenerationDispatcher, request-context binding/pin transfer, and
// manager shutdown/detach primitives for the initial-generation host.
// ShutdownDetached fans out bounded per-generation retirement so a pinned
// generation cannot stall unrelated drained generations.
// Task 7.3 moves retirement scheduling under Manager ownership: Publish fires
// one bounded background retirement per replaced generation (retireGeneration
// in retire.go) without waiting on drain/cleanup, and Manager.RetireGeneration
// is the synchronous retry/wait counterpart used by shutdown and callers that
// need to observe/retry a specific generation's retirement. Each generation
// serializes its own retirement attempts via a context-aware admission gate
// (Generation.retireAdmit) instead of a process-wide lock or worker map.
// Task 4.6 completes retired-generation quiesce, bounded cleanup retry, panic
// isolation, retention-pressure diagnostics, and reverse-order drain/close.
// Task 5.1 adds the production Reload Coordinator: fixed-source read/load,
// no-op and restart-required classification, candidate compile/prepare,
// retention admission, atomic publish, rollback, Busy/coalesce, and
// shutdown-safe terminal status — without duplicating compiler or manager logic.
// Task 5.4 adds ReloadObserver: structured logs, fixed-label metrics, process-owned
// spans, bounded status history, and aggregate generation gauges wired through
// coordinator callbacks — without a second telemetry stack or reload-logic fork.
package runtimehost
