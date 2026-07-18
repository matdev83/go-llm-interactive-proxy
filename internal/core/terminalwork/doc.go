// Package terminalwork implements the pure-domain durable terminal-work item
// state machine (requirements 8.1–8.9, design D9).
//
// Work rows are independently idempotent actions with stable source keys,
// claim leases, and injectable-clock retry schedules. Domain code has no SQL,
// HTTP, or filesystem I/O; stores and processors belong to later tasks.
package terminalwork
