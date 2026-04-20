// Package diag provides request-scoped trace propagation, structured logging helpers,
// and minimal HTTP surfaces for health and B2BUA attempt diagnostics.
//
// Trace and A-leg identifiers are carried on context.Context for use by the executor
// and (in later tasks) frontend encoders and plugins via diag.TraceID / diag.ALegID.
package diag
