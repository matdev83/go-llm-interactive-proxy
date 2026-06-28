package diag

import (
	"context"
	"log/slog"
)

// InterleavedTransition carries bounded, low-cardinality interleaved diagnostics.
// It must never include memo body text, prompt text, or user message content.
type InterleavedTransition struct {
	Phase             string
	Role              string
	MemoPresent       bool
	MemoVisible       bool
	MemoInjected      bool
	MemoExpired       bool
	ThinkerSuppressed bool
	SkipReason        string
	ExtractionSource  string
	StreamInterrupted bool
}

// InterleavedTransitionAttrs builds stable slog attributes for interleaved state transitions.
func InterleavedTransitionAttrs(t InterleavedTransition) []slog.Attr {
	out := make([]slog.Attr, 0, 10)
	if t.Phase != "" {
		out = append(out, slog.String("interleaved_phase", t.Phase))
	}
	if t.Role != "" {
		out = append(out, slog.String("interleaved_role", t.Role))
	}
	out = append(out,
		slog.Bool("memo_present", t.MemoPresent),
		slog.Bool("memo_visible", t.MemoVisible),
		slog.Bool("memo_injected", t.MemoInjected),
		slog.Bool("memo_expired", t.MemoExpired),
		slog.Bool("thinker_suppressed", t.ThinkerSuppressed),
	)
	if t.SkipReason != "" {
		out = append(out, slog.String("memo_skip_reason", t.SkipReason))
	}
	if t.ExtractionSource != "" {
		out = append(out, slog.String("memo_extraction_source", t.ExtractionSource))
	}
	if t.StreamInterrupted {
		out = append(out, slog.Bool("memo_stream_interrupted", true))
	}
	return out
}

// LogInterleavedTransition emits a bounded interleaved diagnostics log line.
func LogInterleavedTransition(ctx context.Context, log *slog.Logger, msg string, o AttrOpts, t InterleavedTransition) {
	if log == nil {
		return
	}
	base := Attrs(ctx, o)
	extra := InterleavedTransitionAttrs(t)
	attrs := make([]slog.Attr, 0, len(base)+len(extra))
	attrs = append(attrs, base...)
	attrs = append(attrs, extra...)
	log.LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
}
