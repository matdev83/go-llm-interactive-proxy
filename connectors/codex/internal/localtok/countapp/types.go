package countapp

import (
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var ErrLocalUnavailable = errors.New("local token counting unavailable")

type CountCallInput struct {
	Backend string
	Model   string
	CallID  string
	Call    lipapi.Call
}

type CountOutputInput struct {
	Backend string
	Model   string
	CallID  string
	Text    string
	Events  []lipapi.Event
}

type CountTextInput struct {
	Backend string
	Model   string
	CallID  string
	Text    string
}

type CountResult struct {
	InputTokens        int
	OutputTokens       int
	CacheReadTokens    int
	CacheWriteTokens   int
	ReasoningTokens    int
	TotalTokens        int
	TotalTokensPresent bool
	Accounting         lipapi.UsageAccountingMetadata
	Fallbacks          []Fallback
}

type FallbackReason string

const FallbackReasonLocalDefaultEncoding FallbackReason = "local_default_encoding"

type Fallback struct {
	Reason  FallbackReason
	Message string
	Err     error
}
