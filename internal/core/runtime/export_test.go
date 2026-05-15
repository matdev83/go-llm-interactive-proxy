package runtime

// Test-only wiring: export_test.go is compiled only for `go test` on internal/core/runtime (same
// test binary as package runtime and co-located runtime_test). Normal imports of runtime omit this
// file (production, runtimebundle, stdhttp, internal/core/runtime/failclosed, etc.), so nil
// SecureSession fails closed there; see failclosed tests for an explicit regression.

import (
	"crypto/rand"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
)

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
