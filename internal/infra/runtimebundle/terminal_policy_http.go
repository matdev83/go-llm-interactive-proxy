package runtimebundle

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	policyhttp "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/terminalpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

const terminalDecisionPolicyFeatureID = "terminal-decision"

// terminalDecisionPolicyHTTPProjection binds the process-owned policy and
// secure-session services to one immutable generation's HTTP composition.
// The callbacks capture only request authority and the process store; they do
// not perform live provider lookup or mutate generation state.
func terminalDecisionPolicyHTTPProjection(
	process candidateProcessRefs,
	snapshot *extensions.RequestRuntimeSnapshot,
	headers lipsdk.HTTPHeaders,
	maxBodyBytes int64,
) httpcontract.TerminalDecisionPolicyInput {
	available := snapshot != nil && snapshot.TerminalDecisionProvider() != nil
	return httpcontract.TerminalDecisionPolicyInput{
		Store: process.terminalDecisionPolicy,
		FeatureStatus: func(_ context.Context, featureID string) (bool, bool, error) {
			return featureID == terminalDecisionPolicyFeatureID, available, nil
		},
		ResolveClientScope: func(ctx context.Context, r *http.Request, featureID string) (terminaldecisionpolicy.Key, terminaldecisionpolicy.Authority, error) {
			return resolveTerminalDecisionClientScope(ctx, r, featureID, process.secureSessions, headers)
		},
		AuthorizeOperatorTarget: func(ctx context.Context, r *http.Request, sessionID, featureID string) (terminaldecisionpolicy.Key, terminaldecisionpolicy.Authority, error) {
			return authorizeTerminalDecisionOperatorTarget(ctx, r, sessionID, featureID, process.secureSessions)
		},
		GenerationDefault: func(featureID string) bool {
			return featureID == terminalDecisionPolicyFeatureID && available
		},
		MaxBodyBytes: maxBodyBytes,
	}
}

func resolveTerminalDecisionClientScope(
	ctx context.Context,
	r *http.Request,
	featureID string,
	store ssessionapp.Store,
	headers lipsdk.HTTPHeaders,
) (terminaldecisionpolicy.Key, terminaldecisionpolicy.Authority, error) {
	sc, ok := httpauth.ScopeFromContext(ctx)
	if !ok || !sc.PrincipalID.IsKnown() || strings.TrimSpace(sc.PrincipalID.String()) == "" {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrUnauthenticated
	}
	if r == nil {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrSecureSessionRequired
	}
	sessionID := strings.TrimSpace(headers.SessionIDValue(r.Header))
	aLegID := strings.TrimSpace(headers.ALegIDValue(r.Header))
	if sessionID == "" || aLegID == "" || store == nil {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrSecureSessionRequired
	}
	rec, err := store.LoadByID(ctx, domain.SessionID(sessionID))
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrSecureSessionRequired
		}
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, err
	}
	if !rec.Status.IsActive() || strings.TrimSpace(rec.ALegID) != aLegID {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrSecureSessionRequired
	}
	if !principalOwnsSession(rec.Owner, sc) {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrForbidden
	}
	key := terminaldecisionpolicy.Key{
		SecureSessionIncarnation: sessionID,
		ALegID:                   rec.ALegID,
		FeatureID:                featureID,
	}
	return key, terminaldecisionpolicy.Authority{
		SecureSessionIncarnation: sessionID,
		ALegID:                   rec.ALegID,
		Authorized:               true,
	}, nil
}

func authorizeTerminalDecisionOperatorTarget(
	ctx context.Context,
	r *http.Request,
	sessionID, featureID string,
	store ssessionapp.Store,
) (terminaldecisionpolicy.Key, terminaldecisionpolicy.Authority, error) {
	if _, ok := httpauth.ScopeFromContext(ctx); !ok {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrUnauthenticated
	}
	if r == nil || store == nil {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrSessionNotFound
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrSessionNotFound
	}
	rec, err := store.LoadByID(ctx, domain.SessionID(sessionID))
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrSessionNotFound
		}
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, err
	}
	if !rec.Status.IsActive() || strings.TrimSpace(rec.ALegID) == "" {
		return terminaldecisionpolicy.Key{}, terminaldecisionpolicy.Authority{}, policyhttp.ErrSessionNotFound
	}
	key := terminaldecisionpolicy.Key{
		SecureSessionIncarnation: sessionID,
		ALegID:                   rec.ALegID,
		FeatureID:                featureID,
	}
	return key, terminaldecisionpolicy.Authority{
		SecureSessionIncarnation: sessionID,
		ALegID:                   rec.ALegID,
		Authorized:               true,
	}, nil
}

func principalOwnsSession(owner domain.PrincipalRef, sc scope.PrincipalScopeView) bool {
	if strings.TrimSpace(owner.ID) == "" || strings.TrimSpace(owner.ID) != strings.TrimSpace(sc.PrincipalID.String()) {
		return false
	}
	if strings.TrimSpace(owner.Tenant) != "" && strings.TrimSpace(owner.Tenant) != strings.TrimSpace(sc.TenantID.String()) {
		return false
	}
	return true
}
