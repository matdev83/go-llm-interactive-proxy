package openairesponses

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type nonstreamOrderKind byte

const (
	nonstreamOrderMessage nonstreamOrderKind = iota
	nonstreamOrderExactReasoning
	nonstreamOrderTool
)

type nonstreamOrderMarker struct {
	kind         nonstreamOrderKind
	exactOrdinal int
	toolCallID   string
}

type nonstreamOutputOrder struct {
	markers       []nonstreamOrderMarker
	messagePlaced bool
	exactCount    int
	seenTools     map[string]struct{}
}

func (o *nonstreamOutputOrder) observe(ev lipapi.Event) {
	switch ev.Kind {
	case lipapi.EventTextDelta, lipapi.EventAssistantImageRef, lipapi.EventAssistantFileRef:
		if o.messagePlaced {
			return
		}
		o.messagePlaced = true
		o.markers = append(o.markers, nonstreamOrderMarker{kind: nonstreamOrderMessage})
	case lipapi.EventReasoningPart:
		if ev.Reasoning == nil {
			return
		}
		if lipapi.NormalizeReasoningDialect(ev.Reasoning.Dialect) != lipapi.ReasoningDialectOpenAIResponsesItemV1 {
			return
		}
		o.markers = append(o.markers, nonstreamOrderMarker{
			kind:         nonstreamOrderExactReasoning,
			exactOrdinal: o.exactCount,
		})
		o.exactCount++
	case lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta:
		id := ev.ToolCallID
		if id == "" {
			return
		}
		if o.seenTools == nil {
			o.seenTools = make(map[string]struct{})
		}
		if _, ok := o.seenTools[id]; ok {
			return
		}
		o.seenTools[id] = struct{}{}
		o.markers = append(o.markers, nonstreamOrderMarker{
			kind:       nonstreamOrderTool,
			toolCallID: id,
		})
	}
}

type orderTeeStream struct {
	inner lipapi.EventStream
	order *nonstreamOutputOrder
}

func (s *orderTeeStream) Recv(ctx context.Context) (lipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	if err != nil {
		return ev, err
	}
	s.order.observe(ev)
	return ev, nil
}

func (s *orderTeeStream) Close() error {
	return s.inner.Close()
}
