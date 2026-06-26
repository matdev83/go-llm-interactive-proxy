package openaicodex

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type usageEstimatingStream struct {
	base     lipapi.ManagedEventStream
	est      *usageEstimator
	call     lipapi.Call
	model    string
	text     strings.Builder
	textFull bool
	sawUsage bool
	queued   *lipapi.Event
}

const maxUsageEstimateOutputBytes = 1 << 20

func newUsageEstimatingStream(base lipapi.ManagedEventStream, est *usageEstimator, call lipapi.Call, model string) lipapi.ManagedEventStream {
	if est == nil {
		return base
	}
	return &usageEstimatingStream{
		base:  base,
		est:   est,
		call:  call,
		model: strings.TrimSpace(model),
	}
}

func (s *usageEstimatingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.queued != nil {
		ev := *s.queued
		s.queued = nil
		return ev, nil
	}
	ev, err := s.base.Recv(ctx)
	if err != nil {
		return ev, err
	}
	s.observe(ev)
	if ev.Kind != lipapi.EventResponseFinished || s.sawUsage || s.textFull {
		return ev, nil
	}
	usage, err := s.est.estimateUsage(ctx, s.call, s.model, s.text.String())
	if err != nil {
		return ev, nil
	}
	s.queued = &ev
	return usage, nil
}

func (s *usageEstimatingStream) observe(ev lipapi.Event) {
	switch ev.Kind {
	case lipapi.EventTextDelta:
		if s.textFull {
			return
		}
		if s.text.Len()+len(ev.Delta) > maxUsageEstimateOutputBytes {
			s.textFull = true
			return
		}
		s.text.WriteString(ev.Delta)
	case lipapi.EventUsageDelta:
		s.sawUsage = true
	}
}

func (s *usageEstimatingStream) Close() error {
	return s.base.Close()
}

func (s *usageEstimatingStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return s.base.Cancel(ctx, cause)
}
