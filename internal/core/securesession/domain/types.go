package domain

import (
	"crypto/subtle"
	"time"
)

// SessionID is a proxy-owned opaque session identifier.
type SessionID string

// ResumeToken is a bearer resume proof (never persist in store records; use TokenFingerprint).
type ResumeToken string

// TokenFingerprint is the stored HMAC-SHA-256 digest of a resume token.
type TokenFingerprint [32]byte

// Equal reports whether a and b are the same fingerprint in constant time.
func (a TokenFingerprint) Equal(b TokenFingerprint) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// PrincipalRef binds a session to an authenticated identity.
type PrincipalRef struct {
	ID     string
	Issuer string
	Tenant string
}

// WorkspaceRef is an optional workspace scope for policy and isolation.
type WorkspaceRef struct {
	ID string
}

// ClientHints are non-authoritative correlation inputs from the client.
type ClientHints struct {
	ClientSessionID     string
	AgentIdentityDigest string
}

// PolicyMetadata captures stored treatment and recording policy flags.
type PolicyMetadata struct {
	PolicyVersion            string
	TranscriptEnabled        bool
	EffectiveTreatment       string
	StricterPolicyResolution string
	RouteHint                string
	RedactionProfile         string
	AuditMode                string
}

// UsageTotals aggregates usage for summaries and diagnostics.
type UsageTotals struct {
	SessionID    SessionID
	InputTokens  int64
	OutputTokens int64
	Attempts     int
}

// UsageDelta increments per-session usage and optional per-attempt accounting fields.
type UsageDelta struct {
	SessionID SessionID
	TurnID    TurnID
	BLegID    string

	InputTokens  int64
	OutputTokens int64

	CacheReadTokens    int64
	CacheWriteTokens   int64
	CostMinorUnits     int64
	Currency           string
	BillingUnavailable bool
}

// TranscriptItem is one ordered transcript entry.
type TranscriptItem struct {
	SessionID  SessionID
	TurnID     TurnID
	Seq        int64
	EventKind  string
	PayloadRef string
	CreatedAt  time.Time
}

// AuditItem is one ordered audit log entry.
type AuditItem struct {
	SessionID SessionID
	TurnID    TurnID
	Seq       int64
	Action    string
	Result    string
	CreatedAt time.Time
}

// TurnID identifies a user turn within a session.
type TurnID string

// ActivitySource classifies what updated last activity.
type ActivitySource string

const (
	ActivityClientRequest ActivitySource = "client_request"
	ActivityRemoteEvent   ActivitySource = "remote_event"
	ActivitySystem        ActivitySource = "system"
)

// SurfaceState describes how a backend attempt was exposed to the client.
type SurfaceState string

const (
	SurfaceSurfaced  SurfaceState = "surfaced"
	SurfaceSwallowed SurfaceState = "swallowed"
	SurfaceFailed    SurfaceState = "failed"
	SurfaceTimeout   SurfaceState = "timeout"
)

// ReadOptions bounds transcript/audit reads.
type ReadOptions struct {
	Limit    int
	AfterSeq int64
}

// SummaryQuery filters session summaries for operators.
type SummaryQuery struct {
	OwnerID     string
	WorkspaceID string
	Limit       int
}

// Summary is a roll-up row for operator views.
type Summary struct {
	SessionID      SessionID
	OwnerID        string
	WorkspaceID    string
	LastActivityAt time.Time
	TurnCount      int
	AttemptCount   int

	ResumeEligible    bool
	ALegID            string
	PolicyVersion     string
	TranscriptEnabled bool
	RedactionProfile  string
	AuditMode         string
	UsageInputTokens  int64
	UsageOutputTokens int64
}

// AttemptSettings snapshots execution-affecting parameters for an attempt.
type AttemptSettings struct {
	Temperature          *float64
	MaxTokens            *int
	Timeout              time.Duration
	ReasoningEffort      string
	Streaming            bool
	ToolSummary          []string
	BackendOptionsDigest string
}

// AttemptTrace captures routing and backend binding at B-leg open.
type AttemptTrace struct {
	SessionID       SessionID
	TurnID          TurnID
	ALegID          string
	BLegID          string
	AttemptSeq      int
	RequestedModel  string
	RequestedAlias  string
	ResolvedBackend string
	ResolvedModel   string
	RouteSource     string
	RouteReason     string
	Settings        AttemptSettings
	StartedAt       time.Time
}

// AttemptOutcome captures terminal state for a B-leg attempt.
type AttemptOutcome struct {
	SessionID      SessionID
	TurnID         TurnID
	BLegID         string
	Success        bool
	SurfaceState   SurfaceState
	HTTPStatus     int
	ProviderStatus string
	ErrorCode      string
	TimeoutClass   string
	DebugReason    string
	EndedAt        time.Time
}

// AttemptAccounting attaches usage/cost signals to a B-leg attempt.
type AttemptAccounting struct {
	BLegID             string
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	CostMinorUnits     int64
	Currency           string
	BillingUnavailable bool
}

// AttemptEvidence joins trace, terminal outcome, and usage for operator diagnostics (Req 14.8).
type AttemptEvidence struct {
	Trace      AttemptTrace
	Outcome    AttemptOutcome
	Accounting AttemptAccounting
}

// Record is persisted secure-session state (no raw resume token).
type Record struct {
	SessionID               SessionID
	ResumeFingerprint       TokenFingerprint
	Owner                   PrincipalRef
	Workspace               WorkspaceRef
	ClientHints             ClientHints
	Policy                  PolicyMetadata
	ALegID                  string
	ResumeEligible          bool
	LastActivityAt          time.Time
	LastActivitySource      ActivitySource
	CreatedAt               time.Time
	LatestAttemptTrace      AttemptTrace
	LatestAttemptOutcome    AttemptOutcome
	LatestAttemptAccounting AttemptAccounting
}

// CreateRecord is input for initial session persistence.
type CreateRecord struct {
	SessionID         SessionID
	ResumeFingerprint TokenFingerprint
	Owner             PrincipalRef
	Workspace         WorkspaceRef
	ClientHints       ClientHints
	Policy            PolicyMetadata
	ALegID            string
	ResumeEligible    bool
	CreatedAt         time.Time
}
