// Package billingbinding adapts one validated public billing.Binding to the
// existing internal runtime chokepoints: the cheap pre-route credit screen,
// the quote/atomic exposure admission authority, and the terminal evidence
// handoff.
//
// The adapter translates internal call facts into public typed DTOs and
// projects binding results back onto internal domain records. It performs no
// rating, journal writes, or provider-field parsing; the binding owns its
// snapshots, workers, and raters. Binding-owned resources start once through
// StartOwnedResources and close once in reverse order through Close; borrowed
// handles are declared only and are never closed.
//
// The reference internal billing composition (ComposeBilling) and an external
// binding occupy the same ProductionOptions chokepoints, so both exercise
// the same runtime admission and terminal paths.
package billingbinding
