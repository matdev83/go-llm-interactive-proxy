package lipapi

import (
	"encoding/json"
	"fmt"
)

// Role identifies who produced a message in the canonical turn sequence.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// SessionRef carries client hints and core continuity identifiers.
type SessionRef struct {
	ClientSessionID string
	ContinuityKey   string
	ALegID          string
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
}

// Validate checks canonical invariants and unsupported combinations for this call.
func (c Call) Validate() error {
	if len(c.Messages) == 0 {
		return &ValidationError{Field: "Messages", Message: "at least one message is required"}
	}
	if err := c.validateEnvelopeSizes(); err != nil {
		return err
	}
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
		}
	}
	for i, t := range c.Tools {
		if t.Name == "" {
			return &ValidationError{Field: fmt.Sprintf("Tools[%d].Name", i), Message: "tool name is required"}
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
