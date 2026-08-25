package contract

import (
	"context"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
)

// TerminalDecisionPolicyInput is the explicit composition projection for the
// provider-neutral client/operator policy routes.
type TerminalDecisionPolicyInput struct {
	Store                   *terminaldecisionpolicy.Store
	FeatureStatus           func(context.Context, string) (known, available bool, err error)
	ResolveClientScope      func(context.Context, *http.Request, string) (terminaldecisionpolicy.Key, terminaldecisionpolicy.Authority, error)
	AuthorizeOperatorTarget func(context.Context, *http.Request, string, string) (terminaldecisionpolicy.Key, terminaldecisionpolicy.Authority, error)
	GenerationDefault       func(string) bool
	MaxBodyBytes            int64
}
