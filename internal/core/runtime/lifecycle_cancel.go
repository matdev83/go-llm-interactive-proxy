package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

// CancelALeg explicitly cancels an active A-leg and all registered B-legs.
func (e *Executor) CancelALeg(ctx context.Context, req lipapi.ALegCancelRequest) error {
	if e == nil {
		return nil
	}
	req = req.Trimmed()
	id := req.ALegID
	if id == "" {
		return leglifecycle.ErrALegCanceled
	}
	if err := e.authorizeALegCancel(ctx, req); err != nil {
		return err
	}
	lifecycle := e.lifecycleCoordinator()
	return lifecycle.CancelALeg(ctx, id, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit, Detail: req.Reason})
}

func (e *Executor) lifecycleCoordinator() *leglifecycle.Coordinator {
	if e == nil {
		return nil
	}
	if e.ALegLifecycle != nil {
		return e.ALegLifecycle
	}
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	if e.ALegLifecycle == nil {
		e.ALegLifecycle = leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 2 * time.Second})
	}
	return e.ALegLifecycle
}

func (e *Executor) authorizeALegCancel(ctx context.Context, req lipapi.ALegCancelRequest) error {
	if e == nil || e.SecureSession == nil {
		return fmt.Errorf("executor: secure session manager is required")
	}
	rec, err := e.SecureSession.LoadByALegID(ctx, req.ALegID)
	if err != nil {
		return err
	}
	if req.SessionID != "" && req.SessionID != string(rec.SessionID) {
		return domain.ErrSessionNotFound
	}
	if p, ok := execview.PrincipalFromContext(ctx); ok {
		if !cancelOwnersMatch(rec.Owner, principalRefFromView(p)) {
			return domain.ErrOwnerMismatch
		}
	} else if !e.SyntheticLocalPrincipal {
		return domain.ErrMissingPrincipal
	}
	return nil
}

func cancelOwnersMatch(stored, want domain.PrincipalRef) bool {
	if strings.TrimSpace(want.ID) == "" {
		return false
	}
	return strings.TrimSpace(stored.ID) == strings.TrimSpace(want.ID) &&
		strings.TrimSpace(stored.Issuer) == strings.TrimSpace(want.Issuer) &&
		strings.TrimSpace(stored.Tenant) == strings.TrimSpace(want.Tenant)
}
