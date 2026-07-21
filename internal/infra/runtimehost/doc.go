// Package runtimehost hosts the process generation manager, leases, lifecycle
// worker, and (later) dispatcher/coordinator surfaces for versioned runtime reload.
//
// Task 3.1 provides production Generation/Lease/Pin/Manager with race-safe
// acquire, atomic publish, transferable pins, and retained-generation budgets.
// Task 3.2 adds LifecycleWorker for post-commit quiesce → drain → close.
package runtimehost
