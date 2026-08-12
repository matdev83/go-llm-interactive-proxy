// Package billing defines provider-neutral immutable usage evidence and billing
// value objects. It owns no runtime orchestration, provider SDK semantics, or
// durable database mechanics; adapters map final provider evidence into these
// values at the boundary.
package billing
