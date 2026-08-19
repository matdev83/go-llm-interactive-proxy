package policy

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// TranscriptAuthorizationFromContext returns the already-authorized scope for
// an optional transcript read. It refuses missing secure-session authority or
// transcript-disabled policy, and it does not fall back to client hints.
func TranscriptAuthorizationFromContext(ctx context.Context) (TranscriptAuthorization, bool) {
	views, trusted := trustedViews(ctx)
	if !trusted {
		return TranscriptAuthorization{}, false
	}
	turn, ok := execctx.SecureSessionTurnFromContext(ctx)
	if !ok || !turn.Policy.TranscriptEnabled {
		return TranscriptAuthorization{}, false
	}
	principal := strings.TrimSpace(views.Scope.PrincipalID.String())
	if principal == "" && views.Scope.SubjectKind != scope.SubjectLocal {
		return TranscriptAuthorization{}, false
	}
	workspaceID := strings.TrimSpace(views.Workspace.ID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(views.Scope.WorkspaceID.String())
	}
	if sessionWorkspace := strings.TrimSpace(views.Session.WorkspaceID); sessionWorkspace != "" && workspaceID != "" && sessionWorkspace != workspaceID {
		return TranscriptAuthorization{}, false
	}
	return TranscriptAuthorization{
		SessionID:   strings.TrimSpace(views.Session.AuthoritativeSessionID),
		PrincipalID: principal,
		TenantID:    strings.TrimSpace(views.Scope.TenantID.String()),
		WorkspaceID: workspaceID,
	}, true
}

// AuthorizeTranscriptWorkspace preserves the existing tenant/workspace scope
// when a reader asks for a specific workspace. Empty requested scope means the
// caller must use the session's already-authorized workspace.
func AuthorizeTranscriptWorkspace(ctx context.Context, requestedWorkspace string) bool {
	return AuthorizeTranscriptScope(ctx, "", requestedWorkspace)
}

// AuthorizeTranscriptScope checks both tenant and workspace ownership. Empty
// requested values mean "use the already-authorized session scope"; a caller
// cannot broaden a read by omitting one dimension.
func AuthorizeTranscriptScope(ctx context.Context, requestedTenant, requestedWorkspace string) bool {
	auth, ok := TranscriptAuthorizationFromContext(ctx)
	if !ok {
		return false
	}
	requestedTenant = strings.TrimSpace(requestedTenant)
	if requestedTenant == "" {
		if auth.TenantID == "" {
			return false
		}
	} else if requestedTenant != auth.TenantID {
		return false
	}
	requestedWorkspace = strings.TrimSpace(requestedWorkspace)
	if requestedWorkspace == "" {
		return auth.WorkspaceID != ""
	}
	return auth.WorkspaceID == requestedWorkspace
}
