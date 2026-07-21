// Package runtimehost hosts the process generation manager, leases, and (later)
// dispatcher/coordinator surfaces for versioned runtime reload.
//
// Task 3.1 provides production Generation/Lease/Pin/Manager with race-safe
// acquire, atomic publish, transferable pins, and retained-generation budgets.
package runtimehost
