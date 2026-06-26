package openaicodex

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
)

const maxBodyBytes = 10 << 20

// Config tunes the Codex Responses emulator.
type Config struct {
	Token            string
	OutputText       string
	PlanType         string
	UsagePercent     string
	ForcedHTTPStatus int
	ForcedRetryAfter string
	ForcedErrorJSON  string
}

// CapturedRequest is a snapshot of the latest handled POST /responses request.
type CapturedRequest struct {
	Path             string
	Authorization    string
	OpenAIBeta       string
	Originator       string
	CodexTaskType    string
	ConversationID   string
	SessionID        string
	ChatGPTAccountID string
	Body             map[string]any
}

// Server is a thread-safe Codex Responses emulator with request capture.
type Server struct {
	cfg Config

	mu                  sync.Mutex
	latest              CapturedRequest
	nextForcedStatus    int
	nextForcedRetry     string
	nextForcedErrorJSON string
}

// New returns an emulator configured from cfg.
func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
	if cfg.ForcedHTTPStatus != 0 {
		s.nextForcedStatus = cfg.ForcedHTTPStatus
		s.nextForcedRetry = cfg.ForcedRetryAfter
		s.nextForcedErrorJSON = cfg.ForcedErrorJSON
	}
	return s
}

// Handler returns the emulator HTTP handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

// LatestRequest returns a copy of the most recently captured request.
func (s *Server) LatestRequest() CapturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.latest
	if s.latest.Body != nil {
		out.Body = maps.Clone(s.latest.Body)
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
		http.NotFound(w, r)
		return
	}

	if s.cfg.Token != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+s.cfg.Token {
			http.Error(w, "missing or invalid bearer", http.StatusUnauthorized)
			return
		}
	}

	if err := validateCodexHeaders(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var payload map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}

	s.mu.Lock()
	s.latest = CapturedRequest{
		Path:             r.URL.Path,
		Authorization:    r.Header.Get("Authorization"),
		OpenAIBeta:       r.Header.Get("OpenAI-Beta"),
		Originator:       r.Header.Get("originator"),
		CodexTaskType:    r.Header.Get("Codex-Task-Type"),
		ConversationID:   r.Header.Get("conversation_id"),
		SessionID:        r.Header.Get("session_id"),
		ChatGPTAccountID: r.Header.Get("chatgpt-account-id"),
		Body:             maps.Clone(payload),
	}
	forcedStatus := s.nextForcedStatus
	forcedRetry := s.nextForcedRetry
	forcedJSON := s.nextForcedErrorJSON
	if forcedStatus != 0 {
		s.nextForcedStatus = 0
		s.nextForcedRetry = ""
		s.nextForcedErrorJSON = ""
	}
	s.mu.Unlock()

	if forcedStatus != 0 {
		writeForcedError(w, forcedStatus, forcedRetry, forcedJSON)
		return
	}

	if s.cfg.PlanType != "" {
		w.Header().Set("x-codex-plan-type", s.cfg.PlanType)
	}
	if s.cfg.UsagePercent != "" {
		w.Header().Set("x-codex-primary-used-percent", s.cfg.UsagePercent)
	}

	writeStream(w, s.outputText())
}

func validateCodexHeaders(r *http.Request) error {
	required := []struct {
		name string
		val  string
	}{
		{"OpenAI-Beta", r.Header.Get("OpenAI-Beta")},
		{"originator", r.Header.Get("originator")},
		{"Codex-Task-Type", r.Header.Get("Codex-Task-Type")},
		{"conversation_id", r.Header.Get("conversation_id")},
		{"session_id", r.Header.Get("session_id")},
	}
	for _, h := range required {
		if strings.TrimSpace(h.val) == "" {
			return fmt.Errorf("missing header %s", h.name)
		}
	}
	return nil
}

func (s *Server) outputText() string {
	if s.cfg.OutputText != "" {
		return s.cfg.OutputText
	}
	return "ok"
}

func writeForcedError(w http.ResponseWriter, status int, retryAfter, body string) {
	if retryAfter != "" && status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == "" {
		body = defaultForcedErrorJSON(status)
	}
	_, _ = io.WriteString(w, body)
}

func defaultForcedErrorJSON(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return `{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`
	case http.StatusTooManyRequests:
		return `{"error":{"message":"rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`
	default:
		return `{"error":{"message":"error","type":"invalid_request_error"}}`
	}
}

func writeStream(w http.ResponseWriter, text string) {
	created := `{"type":"response.created","sequence_number":0,"response":{"id":"resp_codex_ref","object":"response","created_at":1715620000,"status":"in_progress","model":"gpt-5.3-codex"}}`
	delta := fmt.Sprintf(`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_codex_ref","output_index":0,"content_index":0,"delta":%q}`, text)
	completed := fmt.Sprintf(`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_codex_ref","object":"response","created_at":1715620000,"status":"completed","model":"gpt-5.3-codex","output":[{"type":"message","id":"msg_codex_ref","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}`, text)

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "event: response.created\n")
	_, _ = io.WriteString(w, "data: "+created+"\n\n")
	_, _ = io.WriteString(w, "event: response.output_text.delta\n")
	_, _ = io.WriteString(w, "data: "+delta+"\n\n")
	_, _ = io.WriteString(w, "event: response.completed\n")
	_, _ = io.WriteString(w, "data: "+completed+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}
