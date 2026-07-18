package request

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type AttemptDecisionKind string

const (
	AttemptContinue         AttemptDecisionKind = "continue"
	AttemptExcludeCandidate AttemptDecisionKind = "exclude_candidate"
)

type AttemptDecision struct {
	Kind       AttemptDecisionKind
	ReasonCode string
}

type AttemptMeta struct {
	TraceID         string
	ALegID          string
	CandidateKey    string
	BackendID       string
	BackendPrefixes []string
	Model           string
	ReplaySupport   lipapi.ReasoningReplaySupport
	Scope           scope.PrincipalScopeView
	Session         session.SessionView
	Workspace       workspace.WorkspaceView
}

type AttemptTransform interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	HandleAttempt(context.Context, *lipapi.Call, AttemptMeta, Services) (AttemptDecision, error)
}
