package sessionclassification

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

const (
	MaxClientUserAgentBytes = 512
	MaxClassifierIDBytes     = 128
)

type ToolCategorySet uint16

const (
	ToolFileRead ToolCategorySet = 1 << iota
	ToolFileSearch
	ToolOSCommand
	ToolFileEdit
	ToolFileRemove
	ToolWebAccess
	ToolUnknownSeen
)

func (s ToolCategorySet) Has(v ToolCategorySet) bool { return s&v != 0 }

func (s ToolCategorySet) AddToolName(name string) ToolCategorySet {
	cat, _ := lipapi.ClassifyToolName(name)
	switch cat {
	case lipapi.ToolCategoryFileRead:
		return s | ToolFileRead
	case lipapi.ToolCategoryFileSearch:
		return s | ToolFileSearch
	case lipapi.ToolCategoryOSCommand:
		return s | ToolOSCommand
	case lipapi.ToolCategoryFileEdit:
		return s | ToolFileEdit
	case lipapi.ToolCategoryFileRemove:
		return s | ToolFileRemove
	case lipapi.ToolCategoryWebAccess:
		return s | ToolWebAccess
	default:
		return s | ToolUnknownSeen
	}
}

type Evidence struct {
	Operation       lipapi.Operation
	ClientUserAgent string
	ToolCategories  ToolCategorySet
}

func (e Evidence) Validate() error {
	ua := strings.TrimSpace(e.ClientUserAgent)
	if ua != e.ClientUserAgent || len(ua) > MaxClientUserAgentBytes {
		return fmt.Errorf("sessionclassification: invalid client user-agent")
	}
	return nil
}

type Input struct {
	TraceID   string
	Session   session.SessionView
	Workspace workspace.WorkspaceView
	Evidence  Evidence
}

type Classifier interface {
	ID() string
	Classify(context.Context, Input) (session.Classification, error)
}

func ValidateClassifierID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > MaxClassifierIDBytes {
		return fmt.Errorf("sessionclassification: invalid classifier id")
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("sessionclassification: invalid classifier id %q", id)
		}
	}
	return nil
}

func ClassifierIdentity(c Classifier) (string, error) {
	if c == nil {
		return "", fmt.Errorf("sessionclassification: nil classifier")
	}
	id := strings.TrimSpace(c.ID())
	if err := ValidateClassifierID(id); err != nil {
		return "", err
	}
	return id, nil
}
