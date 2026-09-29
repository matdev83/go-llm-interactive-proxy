package controltool

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

var (
	ErrInvalidProvider = errors.New("controltool: invalid provider")
	ErrInvalidSpec     = errors.New("controltool: invalid spec")
	ErrInvalidOutcome  = errors.New("controltool: invalid outcome")
	ErrProjection      = errors.New("controltool: projection failed")
)

// Provider owns one optional proxy-internal model control tool.
type Provider interface {
	ID() string
	Spec() Spec
	Handle(context.Context, CompletedCall, Meta) (Outcome, error)
}

// Spec is the immutable model-facing declaration for one control tool.
type Spec struct {
	Tool         lipapi.ToolDef
	Instruction  Instruction
	MaxArgsBytes int
}

// Instruction is one stable model-facing system/developer instruction.
type Instruction struct {
	Role lipapi.Role
	Text string
}

// CompletedCall is a bounded completed control-tool invocation.
type CompletedCall struct {
	ToolCallID string
	ToolName   string
	ArgsJSON   []byte
}

// Meta carries trusted attempt-local context for a control-tool handler.
type Meta struct {
	TraceID      string
	ALegID       string
	BLegID       string
	CandidateKey string
	AttemptSeq   int

	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace workspace.WorkspaceView
}

// OutcomeKind classifies a handled control call.
type OutcomeKind uint8

const (
	OutcomeInvalid OutcomeKind = iota
	OutcomeComplete
)

// Outcome is private proxy control output. Invalid outcomes never carry client text.
type Outcome struct {
	Kind       OutcomeKind
	ResultText string
	ReasonCode string
}
