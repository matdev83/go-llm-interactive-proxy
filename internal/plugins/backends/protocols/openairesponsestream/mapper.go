package openairesponsestream

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/mediautil"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/responses"
)

// Mapper is not concurrency-safe; callers must serialize Handle calls on a single
// instance. It holds mutable per-stream state including tool-call tracking maps.
type Mapper struct {
	pending *stream.PendingEventQueue

	sawResp      bool
	sawMsg       bool
	sawTextDelta bool

	toolCallStarted   map[string]bool
	toolCallArgDeltas map[string]bool
	toolCallFinished  map[string]bool
}

func New(pending *stream.PendingEventQueue) *Mapper {
	return &Mapper{
		pending:           pending,
		toolCallStarted:   make(map[string]bool),
		toolCallArgDeltas: make(map[string]bool),
		toolCallFinished:  make(map[string]bool),
	}
}

func ToolCallID(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func (m *Mapper) SawResponseStarted() bool {
	return m.sawResp
}

func (m *Mapper) SawTextDelta() bool {
	return m.sawTextDelta
}

func (m *Mapper) EnsureResponseStarted() error {
	return m.ensureResponseStarted()
}

func (m *Mapper) EnsureMessageStarted() error {
	return m.ensureMessageStarted()
}

func (m *Mapper) ResponseCreated() error {
	return m.ensureResponseStarted()
}

func (m *Mapper) OutputTextDelta(delta string) error {
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if err := m.ensureMessageStarted(); err != nil {
		return err
	}
	if delta == "" {
		return nil
	}
	m.sawTextDelta = true
	return m.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: delta})
}

func (m *Mapper) BeginCompleted() error {
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	return m.ensureMessageStarted()
}

func (m *Mapper) CompletedTextFallback(text string) error {
	if m.sawTextDelta || text == "" {
		return nil
	}
	return m.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text})
}

func (m *Mapper) PushUsage(usage *lipapi.Event) error {
	if usage == nil {
		return nil
	}
	return m.pending.Push(*usage)
}

func (m *Mapper) ResponseFinished() error {
	return m.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished})
}

func (m *Mapper) StreamError(code, message, defaultMessage string) error {
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if message == "" {
		message = defaultMessage
	}
	return m.pending.Push(lipapi.Event{
		Kind:         lipapi.EventError,
		ErrorCode:    code,
		ErrorMessage: message,
	})
}

func (m *Mapper) ToolCallAdded(id, name string) error {
	if id == "" {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if m.toolCallStarted[id] {
		return nil
	}
	return m.emitToolCallStarted(id, name)
}

func (m *Mapper) ToolCallArgsDelta(id, delta string) error {
	if id == "" || delta == "" {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if !m.toolCallStarted[id] {
		if err := m.emitToolCallStarted(id, ""); err != nil {
			return err
		}
	}
	m.toolCallArgDeltas[id] = true
	return m.pending.Push(lipapi.Event{
		Kind:       lipapi.EventToolCallArgsDelta,
		ToolCallID: id,
		Delta:      delta,
	})
}

func (m *Mapper) FinishToolCallArguments(id, name, arguments string) error {
	if id == "" {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if !m.toolCallStarted[id] {
		if err := m.emitToolCallStarted(id, name); err != nil {
			return err
		}
	}
	if !m.toolCallArgDeltas[id] && arguments != "" {
		if err := m.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			Delta:      arguments,
		}); err != nil {
			return err
		}
	}
	return m.EmitToolCallFinished(id)
}

func (m *Mapper) EmitCompletedToolCall(id, name, arguments string) error {
	if id == "" || m.toolCallFinished[id] {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if !m.toolCallStarted[id] {
		if err := m.emitToolCallStarted(id, name); err != nil {
			return err
		}
	}
	if !m.toolCallArgDeltas[id] && arguments != "" {
		if err := m.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			Delta:      arguments,
		}); err != nil {
			return err
		}
	}
	return m.EmitToolCallFinished(id)
}

func (m *Mapper) EmitToolCallFinished(id string) error {
	if id == "" {
		return nil
	}
	if m.toolCallFinished[id] {
		return nil
	}
	m.toolCallFinished[id] = true
	return m.pending.Push(lipapi.Event{
		Kind:       lipapi.EventToolCallFinished,
		ToolCallID: id,
	})
}

func (m *Mapper) ensureResponseStarted() error {
	if m.sawResp {
		return nil
	}
	m.sawResp = true
	return m.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
}

func (m *Mapper) ensureMessageStarted() error {
	if m.sawMsg {
		return nil
	}
	m.sawMsg = true
	return m.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
}

func (m *Mapper) emitToolCallStarted(id, name string) error {
	m.toolCallStarted[id] = true
	if err := m.ensureMessageStarted(); err != nil {
		return err
	}
	return m.pending.Push(lipapi.Event{
		Kind:       lipapi.EventToolCallStarted,
		ToolCallID: id,
		ToolName:   name,
	})
}

// EmitOutputMediaFromResponse maps assistant message media in a completed Responses payload.
func EmitOutputMediaFromResponse(m *Mapper, pending *stream.PendingEventQueue, resp responses.Response) error {
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		msg := item.AsMessage()
		for _, c := range msg.Content {
			raw := c.RawJSON()
			if raw == "" {
				continue
			}
			var probe struct {
				Type     string          `json:"type"`
				ImageURL json.RawMessage `json:"image_url"`
				FileID   string          `json:"file_id"`
			}
			if err := json.Unmarshal([]byte(raw), &probe); err != nil {
				continue
			}
			switch probe.Type {
			case "input_image":
				url := mediautil.ExtractImageURL(probe.ImageURL)
				if url == "" {
					continue
				}
				if err := m.EnsureResponseStarted(); err != nil {
					return err
				}
				if err := m.EnsureMessageStarted(); err != nil {
					return err
				}
				if err := pending.Push(lipapi.Event{Kind: lipapi.EventAssistantImageRef, AssistantRef: url, AssistantMIME: mediautil.SniffImageMIME(url)}); err != nil {
					return err
				}
			case "input_file":
				if strings.TrimSpace(probe.FileID) == "" {
					continue
				}
				if err := m.EnsureResponseStarted(); err != nil {
					return err
				}
				if err := m.EnsureMessageStarted(); err != nil {
					return err
				}
				if err := pending.Push(lipapi.Event{Kind: lipapi.EventAssistantFileRef, AssistantRef: probe.FileID, AssistantMIME: "application/octet-stream"}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
