package localstub

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// errPostTextStream is returned after client-visible text when StreamErrorAfterTextDelta is set.
var errPostTextStream = errors.New("local-stub: simulated stream failure after text delta")

const stubToolCallID = "stub-tool-1"

// canonicalEvents returns the ordered canonical stream for one completed assistant turn.
func canonicalEvents(cfg Config) []lipapi.Event {
	evs := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: cfg.Text},
	}
	if cfg.ToolName != "" {
		evs = append(evs,
			lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: stubToolCallID, ToolName: cfg.ToolName},
			lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: stubToolCallID, Delta: `{}`},
			lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: stubToolCallID},
		)
	}
	evs = append(evs,
		lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: cfg.InputTokens, OutputTokens: cfg.OutputTokens},
		lipapi.Event{Kind: lipapi.EventResponseFinished},
	)
	return evs
}

// eventStreamForConfig returns a fixed canonical stream or a truncated-error stream for tests.
func eventStreamForConfig(cfg Config) lipapi.ManagedEventStream {
	if !cfg.StreamErrorAfterTextDelta {
		return lipapi.NewFixedEventStream(canonicalEvents(cfg))
	}
	evs := canonicalEvents(cfg)
	var prefix []lipapi.Event
	for _, e := range evs {
		prefix = append(prefix, e)
		if e.Kind == lipapi.EventTextDelta {
			break
		}
	}
	return &errorAfterPrefixStream{events: prefix}
}

type errorAfterPrefixStream struct {
	events []lipapi.Event
	i      int
}

func (s *errorAfterPrefixStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if s.i >= len(s.events) {
		return lipapi.Event{}, errPostTextStream
	}
	e := s.events[s.i]
	s.i++
	return e, nil
}

func (s *errorAfterPrefixStream) Close() error { return nil }

func (s *errorAfterPrefixStream) Cancel(context.Context, leglifecycle.CancelCause) leglifecycle.CancelResult {
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeNone}
}
