package sessionclassification

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// ToolCategorySet is a fixed bitset of canonical tool-name categories.
type ToolCategorySet uint16

const (
	// ToolCategoryFileRead records at least one canonical file-read tool.
	ToolCategoryFileRead ToolCategorySet = 1 << iota
	// ToolCategoryFileSearch records at least one canonical file-search tool.
	ToolCategoryFileSearch
	// ToolCategoryOSCommand records at least one canonical operating-system command tool.
	ToolCategoryOSCommand
	// ToolCategoryFileEdit records at least one canonical file-edit tool.
	ToolCategoryFileEdit
	// ToolCategoryFileRemove records at least one canonical file-removal tool.
	ToolCategoryFileRemove
	// ToolCategoryWebAccess records at least one canonical web-access tool.
	ToolCategoryWebAccess
	// ToolCategoryUnknownSeen records at least one unrecognized tool name.
	ToolCategoryUnknownSeen
)

// AddToolName returns the set with the canonical category for name added.
// The name itself is not retained; unknown names set ToolCategoryUnknownSeen.
func (s ToolCategorySet) AddToolName(name string) ToolCategorySet {
	category, _ := lipapi.ClassifyToolName(name)
	switch category {
	case lipapi.ToolCategoryFileRead:
		return s | ToolCategoryFileRead
	case lipapi.ToolCategoryFileSearch:
		return s | ToolCategoryFileSearch
	case lipapi.ToolCategoryOSCommand:
		return s | ToolCategoryOSCommand
	case lipapi.ToolCategoryFileEdit:
		return s | ToolCategoryFileEdit
	case lipapi.ToolCategoryFileRemove:
		return s | ToolCategoryFileRemove
	case lipapi.ToolCategoryWebAccess:
		return s | ToolCategoryWebAccess
	default:
		return s | ToolCategoryUnknownSeen
	}
}

// Evidence carries accepted request metadata for one classification evaluation.
// ClientUserAgent must already satisfy the canonical identity acceptance and
// length policy; this SDK contract does not parse headers or define a second
// acceptance policy. Classifiers must not persist the raw User-Agent.
type Evidence struct {
	Operation       lipapi.Operation
	ClientUserAgent string
	ToolCategories  ToolCategorySet
}

// Input is the bounded metadata snapshot presented to a session classifier.
// Session and Workspace are read-only projections. Classifiers may inspect
// bounded workspace markers and labels but must not persist ProjectRoot. No
// request Call, transcript, tool definitions, raw header map, or route state is
// included.
type Input struct {
	TraceID   string
	Session   session.SessionView
	Workspace workspace.WorkspaceView
	Evidence  Evidence
}

// Classifier classifies the current bounded session metadata snapshot.
type Classifier interface {
	// ID returns the stable identifier of this classifier implementation.
	ID() string
	// Classify evaluates one bounded metadata snapshot and returns its projection.
	Classify(context.Context, Input) (session.Classification, error)
}
