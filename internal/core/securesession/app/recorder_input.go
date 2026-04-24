package app

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
)

// ClientInputLine is one ordered accepted client message line after hooks (canonical roles).
type ClientInputLine struct {
	Role    string
	Ordinal int
	Parts   []string // part kinds in order (text, tool_result, …)
}

// ClientTurnRecordInput captures accepted client input for one gated turn.
type ClientTurnRecordInput struct {
	Now       time.Time
	TraceID   string
	SessionID domain.SessionID
	TurnID    domain.TurnID
	Policy    domain.PolicyMetadata
	Lines     []ClientInputLine
}

// StreamEventRecordInput is a post-hook canonical stream slice for recording.
type StreamEventRecordInput struct {
	Now       time.Time
	TraceID   string
	SessionID domain.SessionID
	TurnID    domain.TurnID
	BLegID    string
	BackendID string
	Policy    domain.PolicyMetadata

	EventKind string
	// EventPayloadJSON is a redacted JSON snapshot of the canonical event (never raw resume tokens).
	EventPayloadJSON string

	IsUsageEvent bool
	InputTokens  int64
	OutputTokens int64

	CacheReadTokens    int64
	CacheWriteTokens   int64
	CostMinorUnits     int64
	Currency           string
	BillingUnavailable bool

	// ProviderCorrelationJSON holds non-authoritative provider correlation (never session authority).
	ProviderCorrelationJSON string
}
