package outbound

// This file owns everything a caller may learn about what one outbound pass did.
//
// The whole reporting surface is two values: a closed outcome enum and the shared
// rewriter's content-free statistics. Both are needed and neither may grow a
// content-bearing field, because this is a candidate-level record of a pass that ran
// against untrusted client payloads.
//
// The vocabulary is closed on purpose, and it is closed differently from the
// rewriter's. The rewriter's twelve SkipReason members describe PER-SURFACE
// refusals inside one successful walk: which selector was unresolved, which payload
// was absent. This file's Outcome describes what happened to the PASS as a whole,
// which is a different axis and cannot be expressed as one of those: a call whose
// every surface was refused is still a pass that ran, and a pass that failed before
// it walked anything skipped no surface at all.
//
// The one member that has no counterpart in either existing vocabulary is
// OutcomeTransformationFailed. It is required rather than convenient, and it is
// required by requirements.md 8.2, which asks for a bounded reason specifically when
// outbound virtualization meets an internal error before an alias becomes
// model-visible. DeriveMapping's SkipReason already covers every unusable-root case
// (requirement 1.8's enumeration), so this file adds no new root code: it projects
// that closed set as Report.RootReason rather than widening it.
//
// Nothing here is ever formatted from a payload. Every label is a compile-time
// literal, and every counter comes from the shared rewriter, whose accounting is
// bounded by the closed reason vocabulary and by canonical payload limits
// (requirements.md 7.6, 7.7).

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/rewrite"
)

// Outcome is the bounded, content-free reason one outbound pass ended the way it
// did.
//
// It is an enum because the value only ever reaches fixed-count observability
// dimensions. The set is closed: every member is a condition this pass can actually
// observe, and an unobserved condition cannot invent a new label.
type Outcome uint8

const (
	// OutcomeRewriterRan marks a pass that ran the shared canonical rewriter over
	// the candidate. It is mode-neutral: audit and rewrite both reach it, because
	// audit is the same detection pass with publication switched off
	// (requirements.md 7.3). Whether anything was published is answered by
	// Report.Stats.Rewritten and by the bound rollout mode, never by this value.
	OutcomeRewriterRan Outcome = iota
	// OutcomeProjectRootUnusable marks a pass that published nothing because the
	// authoritative project root is not one of the five supported absolute forms,
	// or is spelled inside the fixed V1 reserved alias namespace. Rewriting is
	// disabled for that mapping rather than guessed at, and the specific bounded
	// code is carried in Report.RootReason (requirement 1.8).
	OutcomeProjectRootUnusable
	// OutcomeTransformationFailed marks a pass that hit an unexpected internal
	// transformation failure before any alias could have become model-visible.
	//
	// This is the fail-open condition of requirements.md 8.2. The candidate is left
	// exactly as the runtime built it, so the real path reaches the backend, and the
	// reason is recorded here instead of being turned into an exclusion: an internal
	// error in an optional optimization must never cost a client its request.
	OutcomeTransformationFailed
)

// String returns the fixed, low-cardinality label of an outcome. It is safe for
// content-free observability dimensions: it never contains path, tool-name, pointer,
// payload, or workspace-identity bytes.
func (o Outcome) String() string {
	switch o {
	case OutcomeRewriterRan:
		return "rewriter_ran"
	case OutcomeProjectRootUnusable:
		return "project_root_unusable"
	case OutcomeTransformationFailed:
		return "transform_failed"
	default:
		return "unknown"
	}
}

// Report is the content-free record of one outbound pass.
//
// Every field is a closed code, a count, or a byte total, so the whole value is safe
// to log, export, or use as a metric dimension. There is deliberately no error
// field: the pass records a bounded REASON for a failure rather than the failure
// itself, so a rewriter error text can never reach a log line, and so the caller
// cannot accidentally surface an internal detail it never inspected
// (requirements.md 7.7).
type Report struct {
	// Outcome is the pass-level verdict. It is always set.
	Outcome Outcome
	// RootReason is the mapper's own bounded refusal code, set only when Outcome is
	// OutcomeProjectRootUnusable. It reuses the lexical core's closed SkipReason
	// vocabulary unchanged, so requirement 1.8's enumeration stays the single
	// authority on why a root is unusable and no second root code exists.
	RootReason pathvirtualization.SkipReason
	// Stats is the shared rewriter's content-free outcome: eligible and rewritten
	// counts, decoded-value byte totals, and per-reason skip tallies over the
	// rewriter's own closed vocabulary. It is zero for every outcome that did not
	// reach the rewriter, and identical between audit and rewrite for the same input.
	Stats rewrite.Stats
}

// Reporter receives one content-free report per outbound pass.
//
// It is declared here, by the consumer, so this package never depends on the
// feature's telemetry, metrics, or diagnostics machinery: requirements.md 9.3 wires
// the real bounded counters, and this task only requires that the outcome and the
// reason are reachable and content-free. An absent reporter is legal and simply
// discards the record.
//
// A reporter may be invoked concurrently. One pass instance is shared by every
// retry, race participant, and failover candidate of a logical A-leg turn, so an
// implementation that keeps counters must be safe for concurrent use.
type Reporter func(Report)

// Option configures one outbound pass at construction.
type Option func(*AttemptTransform)

// WithReporter installs the sink that receives this pass's bounded reports.
//
// It is the only option, and it is optional: a pass built without one still performs
// the whole rewrite and simply records nothing. Making the sink an explicit
// construction parameter rather than package state is what keeps one instance safe to
// share across concurrent candidates, and it keeps this package free of any
// observable mutable state (no init() registration, no globals).
func WithReporter(reporter Reporter) Option {
	return func(t *AttemptTransform) { t.report = reporter }
}
