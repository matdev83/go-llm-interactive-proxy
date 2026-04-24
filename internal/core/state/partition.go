package state

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

func partitionForScope(ctx context.Context, scope lipstate.Scope) (string, error) {
	switch scope {
	case lipstate.ScopeGlobal:
		return "", nil
	case lipstate.ScopeRequest, lipstate.ScopeSession, lipstate.ScopePrincipal:
		v, ok := execctx.FromContext(ctx)
		if !ok {
			return "", lipstate.ErrMissingExecutionContext
		}
		switch scope {
		case lipstate.ScopeRequest:
			t := strings.TrimSpace(v.Attempt.TraceID)
			if t == "" {
				return "", lipstate.ErrMissingExecutionContext
			}
			return t, nil
		case lipstate.ScopeSession:
			s := strings.TrimSpace(v.Session.PartitionKey())
			if s == "" {
				return "", lipstate.ErrMissingExecutionContext
			}
			return s, nil
		case lipstate.ScopePrincipal:
			p := strings.TrimSpace(v.Principal.ID)
			if p == "" {
				return "", lipstate.ErrMissingPrincipal
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("core/state: unknown scope %q", scope)
}
