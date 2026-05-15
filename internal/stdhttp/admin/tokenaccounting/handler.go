package tokenaccounting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const defaultMaxBodyBytes int64 = 1 << 20

// Service is the narrow application seam used by the admin HTTP adapter.
type Service interface {
	Count(context.Context, CountRequest) (CountResponse, error)
}

type countCallService interface {
	CountCall(context.Context, app.CountCallInput) (app.CountResult, error)
}

type Options struct {
	Enabled      bool
	MaxBodyBytes int64
	Service      any
}

type CountRequest struct {
	Backend string      `json:"backend"`
	Model   string      `json:"model"`
	Mode    app.Mode    `json:"mode,omitempty"`
	Call    lipapi.Call `json:"call"`
}

type CountResponse struct {
	Planes []PlaneCount `json:"planes"`
}

type PlaneCount struct {
	Plane             lipapi.UsagePlane     `json:"plane"`
	Tokens            TokenDimensions       `json:"tokens"`
	Source            lipapi.UsageSource    `json:"source"`
	Authority         lipapi.UsageAuthority `json:"authority"`
	Tokenizer         lipapi.TokenizerRef   `json:"tokenizer"`
	UnavailableReason string                `json:"unavailable_reason,omitempty"`
	FallbackReason    app.FallbackReason    `json:"fallback_reason,omitempty"`
}

type TokenDimensions struct {
	Input      int `json:"input,omitempty"`
	Output     int `json:"output,omitempty"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
	Reasoning  int `json:"reasoning,omitempty"`
	Total      int `json:"total,omitempty"`
}

type appServiceAdapter struct {
	svc countCallService
}

func (a appServiceAdapter) Count(ctx context.Context, req CountRequest) (CountResponse, error) {
	result, err := a.svc.CountCall(ctx, app.CountCallInput{
		Backend: req.Backend,
		Model:   req.Model,
		CallID:  req.Call.ID,
		Call:    req.Call,
	})
	if err != nil {
		return CountResponse{}, err
	}
	plane := result.Accounting.Plane
	if plane == lipapi.UsagePlaneUnknown {
		plane = lipapi.UsagePlaneClientVisible
	}
	return CountResponse{Planes: []PlaneCount{{
		Plane: plane,
		Tokens: TokenDimensions{
			Input:      result.InputTokens,
			Output:     result.OutputTokens,
			CacheRead:  result.CacheReadTokens,
			CacheWrite: result.CacheWriteTokens,
			Reasoning:  result.ReasoningTokens,
			Total:      result.TotalTokens,
		},
		Source:    result.Accounting.Source,
		Authority: result.Accounting.Authority,
		Tokenizer: result.Accounting.Tokenizer,
	}}}, nil
}

func NewHandler(opts Options) http.Handler {
	var service Service
	switch svc := opts.Service.(type) {
	case Service:
		service = svc
	case countCallService:
		service = appServiceAdapter{svc: svc}
	}
	maxBodyBytes := opts.MaxBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !opts.Enabled {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, "count_unavailable")
			return
		}

		var req CountRequest
		body := r.Body
		if maxBodyBytes > 0 {
			body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		dec := json.NewDecoder(body)
		if err := dec.Decode(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}

		resp, err := service.Count(r.Context(), req)
		if err != nil {
			writeError(w, statusForError(err), classifyError(err))
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, app.ErrCountingDisabled):
		return http.StatusNotFound
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusServiceUnavailable
	}
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, app.ErrCountingDisabled):
		return "count_disabled"
	case errors.Is(err, context.Canceled):
		return "request_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "count_timeout"
	case errors.Is(err, app.ErrProviderUnsupported):
		return "provider_unsupported"
	case errors.Is(err, app.ErrProviderUnavailable), errors.Is(err, app.ErrLocalUnavailable):
		return "count_unavailable"
	default:
		return "count_unavailable"
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
