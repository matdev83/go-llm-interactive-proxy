package lipapi

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Role identifies who produced a message in the canonical turn sequence.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// SessionRef carries client hints, core continuity identifiers, and optional proxy-owned session authority.
//
// ClientSessionID, ContinuityKey, and ALegID are hints or correlation values unless validated through
// proxy-owned secure-session state. AuthoritativeSessionID is the proxy-owned session id when issued
// by the secure session layer. ResumeToken is a bearer resume proof; it must never be forwarded to backends
// or persisted raw by adapters—only validated via secure-session fingerprints.
//
// JSON name remains "SessionID" for wire compatibility.
type SessionRef struct {
	ClientSessionID        string
	ContinuityKey          string
	ALegID                 string
	AuthoritativeSessionID string `json:"SessionID,omitempty"`
	ResumeToken            string
}

// CorrelationID returns a stable identifier for diagnostics and traffic capture: authoritative id when set, otherwise the client hint.
func (s SessionRef) CorrelationID() string {
	if x := strings.TrimSpace(s.AuthoritativeSessionID); x != "" {
		return x
	}
	return strings.TrimSpace(s.ClientSessionID)
}

// RouteIntent captures routing input produced by a frontend decoder.
// The planner owns interpretation; this stays an opaque intent string at the API layer.
type RouteIntent struct {
	Selector string
}

// Message is one ordered turn in the conversation.
type Message struct {
	Role  Role
	Parts []Part
}

// Call is the canonical request envelope shared across frontends.
type Call struct {
	ID           string
	Session      SessionRef
	Route        RouteIntent
	Instructions []Message
	Messages     []Message
	Tools        []ToolDef
	ToolChoice   ToolChoice
	Options      GenerationOptions
	Extensions   map[string]json.RawMessage
	Invocation   Invocation `json:"-"`

	// MaxPendingWireEvents caps backend adapter-internal pending event queues per stream (0 = unlimited).
	// Not client API; the core executor sets this from server config when non-zero.
	MaxPendingWireEvents int `json:"-"`
}

// Validate checks canonical invariants and unsupported combinations for this call.
func (c Call) Validate() error {
	if len(c.Messages) == 0 {
		return &ValidationError{Field: "Messages", Message: "at least one message is required"}
	}
	if err := c.validateEnvelopeSizes(); err != nil {
		return err
	}
	var reasoningBytes int64
	for i, m := range c.Messages {
		if m.Role == "" {
			return &ValidationError{Field: fmt.Sprintf("Messages[%d].Role", i), Message: "role is required"}
		}
		if len(m.Parts) == 0 {
			return &ValidationError{Field: fmt.Sprintf("Messages[%d].Parts", i), Message: "at least one part is required"}
		}
		for j, p := range m.Parts {
			if err := p.validate(); err != nil {
				return &ValidationError{Field: fmt.Sprintf("Messages[%d].Parts[%d]", i, j), Message: err.Error()}
			}
			if p.Kind == PartReasoning {
				if m.Role != RoleAssistant {
					return &ValidationError{
						Field:   fmt.Sprintf("Messages[%d].Parts[%d]", i, j),
						Message: "reasoning parts are only allowed on assistant messages",
					}
				}
				reasoningBytes = addSaturatingInt64(reasoningBytes, int64(ReasoningPayloadBytes(p.Reasoning)))
				if reasoningBytes > int64(MaxReasoningBytesPerCall) {
					return &ValidationError{
						Field:   fmt.Sprintf("Messages[%d].Parts[%d]", i, j),
						Message: fmt.Sprintf("total reasoning payload exceeds %d bytes", MaxReasoningBytesPerCall),
					}
				}
			}
		}
	}
	for i, m := range c.Instructions {
		if m.Role == "" {
			return &ValidationError{Field: fmt.Sprintf("Instructions[%d].Role", i), Message: "role is required"}
		}
		if len(m.Parts) == 0 {
			return &ValidationError{Field: fmt.Sprintf("Instructions[%d].Parts", i), Message: "at least one part is required"}
		}
		for j, p := range m.Parts {
			if err := p.validate(); err != nil {
				return &ValidationError{Field: fmt.Sprintf("Instructions[%d].Parts[%d]", i, j), Message: err.Error()}
			}
			if p.Kind == PartReasoning {
				if m.Role != RoleAssistant {
					return &ValidationError{
						Field:   fmt.Sprintf("Instructions[%d].Parts[%d]", i, j),
						Message: "reasoning parts are only allowed on assistant messages",
					}
				}
				reasoningBytes = addSaturatingInt64(reasoningBytes, int64(ReasoningPayloadBytes(p.Reasoning)))
				if reasoningBytes > int64(MaxReasoningBytesPerCall) {
					return &ValidationError{
						Field:   fmt.Sprintf("Instructions[%d].Parts[%d]", i, j),
						Message: fmt.Sprintf("total reasoning payload exceeds %d bytes", MaxReasoningBytesPerCall),
					}
				}
			}
		}
	}
	for i, t := range c.Tools {
		if t.Name == "" {
			return &ValidationError{Field: fmt.Sprintf("Tools[%d].Name", i), Message: "tool name is required"}
		}
		if len(t.Parameters) > 0 && !json.Valid(t.Parameters) {
			return &ValidationError{Field: fmt.Sprintf("Tools[%d].Parameters", i), Message: "parameters must be valid JSON when set"}
		}
	}
	if err := c.ToolChoice.validate(len(c.Tools), c.Tools); err != nil {
		return err
	}
	if err := c.Options.validate(); err != nil {
		return err
	}
	return nil
}

func addSaturatingInt64(a, b int64) int64 {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// SaturatingAddInt64 returns a+b clamped at math.MaxInt64 (negatives treated as 0).
func SaturatingAddInt64(a, b int64) int64 { return addSaturatingInt64(a, b) }

// CallReasoningPayloadBytes returns the saturating sum of reasoning Text+Signature+Opaque
// lengths across Messages and Instructions (dialect and non-reasoning content excluded).
func CallReasoningPayloadBytes(c *Call) int64 {
	if c == nil {
		return 0
	}
	var n int64
	for _, m := range c.Messages {
		for _, p := range m.Parts {
			if p.Kind == PartReasoning {
				n = addSaturatingInt64(n, int64(ReasoningPayloadBytes(p.Reasoning)))
			}
		}
	}
	for _, m := range c.Instructions {
		for _, p := range m.Parts {
			if p.Kind == PartReasoning {
				n = addSaturatingInt64(n, int64(ReasoningPayloadBytes(p.Reasoning)))
			}
		}
	}
	return n
}
