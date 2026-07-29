package openairesponses

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openaiwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EncodeOptions controls wire identifiers for encoded Responses payloads.
type EncodeOptions struct {
	ResponseID               string
	MessageID                string
	CreatedAt                int64
	ExposeLipUsageExtensions bool
}

func defaultEncodeOptions(call *lipapi.Call, opts EncodeOptions) EncodeOptions {
	if opts.ResponseID == "" {
		opts.ResponseID = "resp_" + diag.StableCallToken(call)
	}
	if opts.MessageID == "" {
		opts.MessageID = "msg_" + opts.ResponseID
	}
	if opts.CreatedAt == 0 {
		opts.CreatedAt = diag.StableUnix(call)
	}
	return opts
}

// WriteErrorJSON writes an OpenAI-shaped JSON error before any streamed bytes.
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType, code string) error {
	return openaiwire.WriteErrorJSON(w, status, message, errType, code)
}
