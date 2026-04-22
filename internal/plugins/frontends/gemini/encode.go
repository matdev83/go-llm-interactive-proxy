package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// gemStreamWireScratch holds reusable wire buffers for WriteStreamSSE.
// It must not escape the WriteStreamSSE call stack; FlushSSEDataJSON returns before any other use.
type gemStreamWireScratch struct {
	frame     gemCandStreamWire
	cands     [1]gemCandItem
	textParts [1]gemCandPart
	toolParts [1]gemCandPart
	fc        gemFuncCallWire
}

func (s *gemStreamWireScratch) initFrame() {
	s.frame.Candidates = s.cands[:]
	s.cands[0].Content.Role = "model"
}

func (s *gemStreamWireScratch) flushTextDelta(w io.Writer, fl http.Flusher, delta string) error {
	s.textParts[0].Text = delta
	s.textParts[0].FunctionCall = nil
	s.textParts[0].FileData = nil
	s.cands[0].Content.Parts = s.textParts[:1]
	return stream.FlushSSEDataJSON(w, fl, s.frame)
}

func (s *gemStreamWireScratch) flushToolCall(w io.Writer, fl http.Flusher, name, argsStr string) error {
	args, err := toolArgsToRawJSON(argsStr)
	if err != nil {
		return err
	}
	s.fc.Name = name
	s.fc.Args = args
	s.toolParts[0].Text = ""
	s.toolParts[0].FunctionCall = &s.fc
	s.toolParts[0].FileData = nil
	s.cands[0].Content.Parts = s.toolParts[:1]
	return stream.FlushSSEDataJSON(w, fl, s.frame)
}

func (s *gemStreamWireScratch) flushFileDataURI(w io.Writer, fl http.Flusher, uri, mime string) error {
	if mime == "" {
		mime = "application/octet-stream"
	}
	s.textParts[0].Text = ""
	s.textParts[0].FunctionCall = nil
	s.textParts[0].FileData = &gemFileDataWire{MIMEType: mime, FileURI: uri}
	s.cands[0].Content.Parts = s.textParts[:1]
	return stream.FlushSSEDataJSON(w, fl, s.frame)
}

// EncodeOptions controls optional encoding tweaks.
type EncodeOptions struct{}

// gemCandStreamWire matches the streaming generateContent SSE JSON shape.
type gemCandStreamWire struct {
	Candidates []gemCandItem `json:"candidates"`
}

type gemCandItem struct {
	Content gemCandContent `json:"content"`
}

type gemCandContent struct {
	Role  string        `json:"role"`
	Parts []gemCandPart `json:"parts"`
}

type gemFileDataWire struct {
	MIMEType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gemCandPart struct {
	Text         string           `json:"text,omitempty"`
	FunctionCall *gemFuncCallWire `json:"functionCall,omitempty"`
	FileData     *gemFileDataWire `json:"fileData,omitempty"`
}

type gemFuncCallWire struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gemUsageStreamWire struct {
	UsageMetadata gemUsageMeta `json:"usageMetadata"`
}

type gemUsageMeta struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func toolArgsToRawJSON(raw string) (json.RawMessage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return json.RawMessage("{}"), nil
	}
	var probe json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		type wrap struct {
			Value string `json:"value"`
		}
		return json.Marshal(wrap{Value: raw})
	}
	return json.RawMessage(raw), nil
}

func buildGenerateContentWire(col lipapi.Collected) (gemCandStreamWire, error) {
	text := col.Text.String()
	tools := col.OrderedToolCalls()
	parts := make([]gemCandPart, 0, len(tools)+1+len(col.AssistantMedia))
	if text != "" {
		parts = append(parts, gemCandPart{Text: text})
	}
	for _, tc := range tools {
		args, err := toolArgsToRawJSON(tc.Arguments)
		if err != nil {
			return gemCandStreamWire{}, err
		}
		parts = append(parts, gemCandPart{
			FunctionCall: &gemFuncCallWire{Name: tc.Name, Args: args},
		})
	}
	for _, p := range col.AssistantMedia {
		switch p.Kind {
		case lipapi.PartImageRef, lipapi.PartFileRef:
			ref := p.ImageRef
			mime := p.ImageMIME
			if p.Kind == lipapi.PartFileRef {
				ref = p.FileRef
				mime = p.FileMIME
			}
			if mime == "" {
				mime = "application/octet-stream"
			}
			parts = append(parts, gemCandPart{FileData: &gemFileDataWire{MIMEType: mime, FileURI: ref}})
		}
	}
	if len(parts) == 0 {
		parts = append(parts, gemCandPart{Text: ""})
	}
	return gemCandStreamWire{
		Candidates: []gemCandItem{{
			Content: gemCandContent{Role: "model", Parts: parts},
		}},
	}, nil
}

// WriteNonStreamJSON encodes a completed canonical stream as a generateContent JSON body.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, _ EncodeOptions) error {
	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		return err
	}
	resp, err := buildGenerateContentWire(col)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

// WriteStreamSSE emits Gemini stream chunks incrementally from the canonical stream.
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, _ EncodeOptions) (err error) {
	ka, err := stream.WrapRecoveryKeepalive(es)
	if err != nil {
		return err
	}
	es = ka
	defer func() {
		if cerr := es.Close(); cerr != nil {
			closeErr := fmt.Errorf("gemini: close event stream: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("gemini: ResponseWriter is not a Flusher")
	}

	var inTok, outTok int
	toolArgs := make(map[string]*strings.Builder)
	toolNames := make(map[string]string)

	var scratch gemStreamWireScratch
	scratch.initFrame()

	var ev lipapi.Event
	for {
		ev, err = es.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("gemini: stream ended without response_finished")
		}
		if err != nil {
			return err
		}
		switch ev.Kind {
		case lipapi.EventTextDelta:
			if err := scratch.flushTextDelta(w, fl, ev.Delta); err != nil {
				return err
			}
		case lipapi.EventAssistantImageRef, lipapi.EventAssistantFileRef:
			mime := ev.AssistantMIME
			if mime == "" {
				mime = "application/octet-stream"
			}
			if err := scratch.flushFileDataURI(w, fl, ev.AssistantRef, mime); err != nil {
				return err
			}
		case lipapi.EventToolCallStarted:
			if ev.ToolCallID != "" && ev.ToolName != "" {
				toolNames[ev.ToolCallID] = ev.ToolName
			}
		case lipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "" {
				continue
			}
			b := toolArgs[ev.ToolCallID]
			if b == nil {
				b = new(strings.Builder)
				toolArgs[ev.ToolCallID] = b
			}
			b.WriteString(ev.Delta)
		case lipapi.EventToolCallFinished:
			if ev.ToolCallID == "" {
				continue
			}
			name := toolNames[ev.ToolCallID]
			argsStr := ""
			if b := toolArgs[ev.ToolCallID]; b != nil {
				argsStr = b.String()
			}
			if err := scratch.flushToolCall(w, fl, name, argsStr); err != nil {
				return err
			}
		case lipapi.EventUsageDelta:
			inTok += ev.InputTokens
			outTok += ev.OutputTokens
		case lipapi.EventResponseFinished:
			if inTok > 0 || outTok > 0 {
				var u gemUsageStreamWire
				u.UsageMetadata.PromptTokenCount = inTok
				u.UsageMetadata.CandidatesTokenCount = outTok
				u.UsageMetadata.TotalTokenCount = inTok + outTok
				if err := stream.FlushSSEDataJSON(w, fl, u); err != nil {
					return err
				}
			}
			return nil
		case lipapi.EventError:
			return lipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage)
		case lipapi.EventResponseStarted, lipapi.EventMessageStarted:
		case lipapi.EventWarning:
			if ev.WarningCode == stream.KeepaliveEventCode {
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					return err
				}
				fl.Flush()
				continue
			}
		case lipapi.EventReasoningDelta:
		default:
		}
	}
}
