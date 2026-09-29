package session

import (
	"fmt"
	"strings"
)

// Kind is derived advisory session metadata. It is never authentication,
// authorization, routing authority, or a substitute for an authoritative
// secure-session identifier.
type Kind string

const (
	KindUnknown     Kind = ""
	KindCodingAgent Kind = "coding_agent"
)

type ClassificationSource string

const (
	SourceLocalIdentity ClassificationSource = "local_identity"
	SourceLocalTooling  ClassificationSource = "local_tooling"
	SourceRemote        ClassificationSource = "remote_classifier"
)

type ConfidenceBand string

const (
	ConfidenceUnknown ConfidenceBand = ""
	ConfidenceHigh    ConfidenceBand = "high"
)

type EvidenceCode string

const MaxEvidenceCodeBytes = 64

// Classification is a bounded, monotonic derived label persisted per
// authoritative session (or proxy-owned A-leg when no secure SessionID exists).
// The all-zero value is the canonical unknown state.
type Classification struct {
	Kind       Kind
	Source     ClassificationSource
	Confidence ConfidenceBand
	Evidence   EvidenceCode
	Revision   uint64
}

func (c Classification) IsCodingAgent() bool { return c.Kind == KindCodingAgent }

func (c Classification) IsUnknown() bool {
	return c.Kind == KindUnknown && c.Source == "" && c.Confidence == "" && c.Evidence == "" && c.Revision == 0
}

func (c Classification) Validate() error {
	if c.IsUnknown() {
		return nil
	}
	if c.Kind != KindCodingAgent {
		return fmt.Errorf("session: invalid classification kind %q", c.Kind)
	}
	switch c.Source {
	case SourceLocalIdentity, SourceLocalTooling, SourceRemote:
	default:
		return fmt.Errorf("session: invalid classification source %q", c.Source)
	}
	if c.Confidence != ConfidenceHigh {
		return fmt.Errorf("session: invalid classification confidence %q", c.Confidence)
	}
	ev := strings.TrimSpace(string(c.Evidence))
	if ev == "" || len(ev) > MaxEvidenceCodeBytes || ev != string(c.Evidence) {
		return fmt.Errorf("session: invalid classification evidence code")
	}
	for _, r := range ev {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return fmt.Errorf("session: invalid classification evidence code")
		}
	}
	if c.Revision == 0 {
		return fmt.Errorf("session: positive classification revision must be non-zero")
	}
	return nil
}
