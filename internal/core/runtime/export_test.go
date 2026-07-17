package runtime

// Test-only wiring: export_test.go is compiled only for `go test` on internal/core/runtime (same
// test binary as package runtime and co-located runtime_test). Normal imports of runtime omit this
// file (production, runtimebundle, stdhttp, internal/core/runtime/failclosed, etc.), so nil
// SecureSession fails closed there; see failclosed tests for an explicit regression.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// SecretGuardStageInputForTest is the exported mirror of secretGuardStageInput.
type SecretGuardStageInputForTest struct {
	TraceID   string
	Principal execview.PrincipalView
	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace lipworkspace.WorkspaceView
	SessionID domain.SessionID
	TurnID    domain.TurnID
}

func init() {
	secureSessionTestPrepare = prepareExecutorSecureSessionForTests
}

func (e *Executor) ResolveAffinityKeyForTest(mode routing.AffinityMode, views execctx.Views, viewsOK bool) (affinity.Key, bool, error) {
	return e.resolveAffinityKey(&routing.Selector{Affinity: mode}, views, viewsOK)
}

func prepareExecutorSecureSessionForTests(e *Executor) {
	if e == nil || e.SecureSession != nil {
		return
	}
	if e.Store == nil {
		panic("runtime test wiring requires a non-nil B2BUA store on the executor")
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := make([]byte, 32)
	if _, err := rand.Read(fk); err != nil {
		for i := range fk {
			fk[i] = byte(i + 1)
		}
	}
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(e.Store), app.ManagerConfig{
		FingerprintKey: fk,
		StoreDurable:   true,
	})
	if err != nil {
		panic(fmt.Sprintf("runtime: test secure-session wiring: %v", err))
	}
	e.SecureSession = mgr
	if e.SessionDenialMapper == nil {
		e.SessionDenialMapper = lipapidenial.MapToSessionDenial
	}
	e.SyntheticLocalPrincipal = true
}

type ParallelPreWinFailStream struct{}

func (ParallelPreWinFailStream) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, errors.New("parallel leg failed before winner")
}

func (ParallelPreWinFailStream) Close() error { return nil }

func (ParallelPreWinFailStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

// RunSecretGuardStageForTest exposes runSecretGuardStage for invariant tests (empty SessionID, etc.).
func (e *Executor) RunSecretGuardStageForTest(ctx context.Context, call *lipapi.Call, in SecretGuardStageInputForTest) error {
	return e.runSecretGuardStage(ctx, call, secretGuardStageInput(in))
}

// ApplySecretGuardBlockForTest exposes the block finalization pipeline for precedence tests.
func (e *Executor) ApplySecretGuardBlockForTest(ctx context.Context, call *lipapi.Call, in SecretGuardStageInputForTest, block *extensions.SecretGuardBlockInfo) error {
	meta := secretguard.Meta{
		TraceID:   in.TraceID,
		Principal: in.Principal,
		Scope:     in.Scope,
		Session:   in.Session,
		Workspace: in.Workspace,
	}
	return e.applySecretGuardBlock(ctx, e.secretGuardAudit(in.TurnID), meta, call, block, secretGuardStageInput(in))
}

// ToolFinalActiveCountForTest returns residual assembler map/drain sizes for the
// retryRecvStream under stream (0,0,0,0 when cleared or inactive). Used to prove
// markFinished clears attempt-local finalizer state on normal response_finished.
func ToolFinalActiveCountForTest(stream lipapi.EventStream) (active, passThrough, completed, drain int) {
	rs, ok := stream.(*retryRecvStream)
	if !ok || rs == nil || rs.toolFinal == nil {
		return 0, 0, 0, 0
	}
	a := rs.toolFinal
	return len(a.active), len(a.passThrough), len(a.completed), len(a.drain)
}
