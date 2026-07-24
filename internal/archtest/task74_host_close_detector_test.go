package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Task 7.4 detector fixtures: synthetic positives prove the gate catches
// renamed, aliased, nested, callback, factory, global, and direct-typed
// evasions; synthetic negatives prove unrelated resource Close, management
// Shutdown, http.Server Shutdown, reload-only Host use, and the sole
// shape-based pre-Host rollback stay valid.

func mustParseHCFiles(t *testing.T, sources map[string]string) map[string]*ast.File {
	t.Helper()
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out[name] = f
	}
	return out
}

const hcOwnerHostDecl = `
package runtimebundle
import "context"
type Manager struct{}
func (m *Manager) ShutdownDetached(ctx context.Context) error { return nil }
func (m *Manager) BeginShutdown() {}
func (m *Manager) Active() *Manager { return nil }
type ProcessServices struct{}
func (p *ProcessServices) Close() error { return nil }
func (p *ProcessServices) Closed() bool { return false }
type Coordinator struct{}
func (c *Coordinator) BeginShutdown() {}
func (c *Coordinator) WaitForIdle(ctx context.Context) error { return nil }
type ReloadHost struct {
	Coordinator *Coordinator
	Manager *Manager
	Process *ProcessServices
	ShutdownTracing func(context.Context) error
}
type Host = ReloadHost
func (h *ReloadHost) BeginShutdown() { h.Coordinator.BeginShutdown() }
func (h *ReloadHost) WaitForIdle(ctx context.Context) error { return h.Coordinator.WaitForIdle(ctx) }
func (h *ReloadHost) Close(ctx context.Context) error {
	h.BeginShutdown()
	if err := h.WaitForIdle(ctx); err != nil { return err }
	if err := h.Manager.ShutdownDetached(ctx); err != nil { return err }
	if err := h.closeProcessOnce(); err != nil { return err }
	return h.shutdownTracingOnce(ctx)
}
func (h *ReloadHost) closeProcessOnce() error { return h.Process.Close() }
func (h *ReloadHost) shutdownTracingOnce(ctx context.Context) error { return h.ShutdownTracing(ctx) }
`

const hcPreHostOwnerDecl = `
func joinInitialFailureCleanup(ctx context.Context, primary error, genRollback func() error, processClose func() error, traceShutdown func(context.Context) error) error {
	if genRollback != nil { _ = genRollback() }
	if processClose != nil { _ = processClose() }
	if traceShutdown != nil { _ = traceShutdown(ctx) }
	return primary
}
`

func hcOwnerZone() hcZone { return hcZone{Scope: "internal/infra/runtimebundle", HostOwner: true} }

func hcConsumerZone() hcZone { return hcZone{Scope: "cmd/lipstd"} }

func TestTask74Detector_OwnerCanonicalCloseIsAccepted(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{"host.go": hcOwnerHostDecl})
	if got := analyzeHostCloseOwnership(hcOwnerZone(), files); len(got) != 0 {
		t.Fatalf("canonical Host.Close must pass; got %v", got)
	}
	rep := reportHostCloseOwnership(hcOwnerZone(), files)
	if roots := rep.Roots(); len(roots) != 1 || roots[0] != "ReloadHost.Close" {
		t.Fatalf("roots=%v want [ReloadHost.Close]", roots)
	}
}

func TestTask74Detector_OwnerRejectsSecondShutdownWorkflow(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl,
		"rogue.go": `
package runtimebundle
import "context"
func teardownEverything(ctx context.Context, h *ReloadHost) error {
	h.Coordinator.BeginShutdown()
	_ = h.Manager.ShutdownDetached(ctx)
	_ = h.Process.Close()
	return h.ShutdownTracing(ctx)
}
`,
	})
	got := analyzeHostCloseOwnership(hcOwnerZone(), files)
	if !hcFindingsHave(got, "teardownEverything") {
		t.Fatalf("expected renamed second shutdown workflow rejection; got %v", got)
	}
}

func TestTask74Detector_RejectsAliasAndLocalHandleEvasions(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl,
		"rogue.go": `
package runtimebundle
import "context"
type hostAlias = ReloadHost
func viaAlias(ctx context.Context, h *hostAlias) error {
	mgr := h.Manager
	ps := h.Process
	if err := mgr.ShutdownDetached(ctx); err != nil { return err }
	return ps.Close()
}
`,
	})
	got := analyzeHostCloseOwnership(hcOwnerZone(), files)
	if !hcFindingsHave(got, "viaAlias") {
		t.Fatalf("expected alias/local-handle evasion rejection; got %v", got)
	}
}

func TestTask74Detector_RejectsNestedStructAndGlobalStorage(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl,
		"rogue.go": `
package runtimebundle
import "context"
type inner struct{ host *ReloadHost }
type outer struct{ inner }
var current *Host
func (o *outer) Drain(ctx context.Context) error { return o.host.Manager.ShutdownDetached(ctx) }
func globalDrain(ctx context.Context) error { return current.Process.Close() }
`,
	})
	got := analyzeHostCloseOwnership(hcOwnerZone(), files)
	for _, want := range []string{"outer.Drain", "globalDrain"} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected %s rejection; got %v", want, got)
		}
	}
}

func TestTask74Detector_RejectsMethodValueCallbackAndFactoryEscapes(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl,
		"rogue.go": `
package runtimebundle
import "context"
func register(fn func() error) {}
func registerTracing(fn func(context.Context) error) {}
func newHost() *ReloadHost { return nil }
func stash(h *ReloadHost) {
	register(h.Process.Close)
	registerTracing(h.ShutdownTracing)
}
func viaFactory(ctx context.Context) error { return newHost().Manager.ShutdownDetached(ctx) }
func viaClosure(h *ReloadHost) func(context.Context) error {
	return func(ctx context.Context) error { return h.Manager.ShutdownDetached(ctx) }
}
`,
	})
	got := analyzeHostCloseOwnership(hcOwnerZone(), files)
	for _, want := range []string{"stash", "viaFactory", "viaClosure"} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected %s rejection; got %v", want, got)
		}
	}
}

func TestTask74Detector_RejectsConsumerDecomposition(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import (
	"context"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)
type facade struct{ host *runtimebundle.Host }
func serve(ctx context.Context, host *runtimebundle.Host) error {
	host.Coordinator.BeginShutdown()
	_ = host.Manager.ShutdownDetached(ctx)
	_ = host.Process.Close()
	return host.ShutdownTracing(ctx)
}
func (f *facade) Close(ctx context.Context) error { return f.host.Process.Close() }
`,
	})
	got := analyzeHostCloseOwnership(hcConsumerZone(), files)
	for _, want := range []string{"serve", "facade.Close"} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected %s rejection; got %v", want, got)
		}
	}
}

func TestTask74Detector_RejectsConsumerPassingHostInternals(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import (
	"context"
	rb "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)
type serveInput struct {
	Manager any
	Process any
}
func run(in serveInput) error { return nil }
func serve(ctx context.Context, host *rb.ReloadHost) error {
	return run(serveInput{Manager: host.Manager, Process: host.Process})
}
`,
	})
	got := analyzeHostCloseOwnership(hcConsumerZone(), files)
	if !hcFindingsHave(got, "serve") {
		t.Fatalf("expected host internal pass-through rejection; got %v", got)
	}
}

func TestTask74Detector_AcceptsConsumerHostCloseSeam(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import (
	"context"
	"net/http"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)
type facade struct{ host *runtimebundle.Host }
type mgmt interface{ Shutdown(context.Context) error }
func serve(ctx context.Context, host *runtimebundle.Host, m mgmt, srv *http.Server) error {
	if err := srv.Shutdown(ctx); err != nil { return err }
	if err := m.Shutdown(ctx); err != nil { return err }
	host.BeginShutdown()
	_ = host.Reload(ctx, sdkreload.Trigger{})
	_ = host.Status()
	_ = host.FixedSourcePath()
	_ = host.HTTPHandler()
	return host.Close(ctx)
}
func (f *facade) Close(ctx context.Context) error { return f.host.Close(ctx) }
`,
	})
	if got := analyzeHostCloseOwnership(hcConsumerZone(), files); len(got) != 0 {
		t.Fatalf("host close seam + reload-only Host capabilities must pass; got %v", got)
	}
}

func TestTask74Detector_RejectsConsumerCoordinatorPassThrough(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func wire(host *runtimebundle.Host) any { return host.Coordinator }
func stashManager(host *runtimebundle.Host) any { return host.Manager }
func stashProcess(host *runtimebundle.Host) any { return host.Process }
func stashTracing(host *runtimebundle.Host) any { return host.ShutdownTracing }
`,
	})
	got := analyzeHostCloseOwnership(hcConsumerZone(), files)
	for _, want := range []string{"wire", "stashManager", "stashProcess", "stashTracing"} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected %s Host-field pass-through rejection; got %v", want, got)
		}
	}
}

func TestTask74Detector_RejectsDirectTypedManagerProcessInConsumer(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import (
	"context"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)
func rogue(ctx context.Context, m *runtimehost.Manager, p *runtimebundle.ProcessServices) error {
	_ = m.ShutdownDetached(ctx)
	return p.Close()
}
`,
	})
	got := analyzeHostCloseOwnership(hcConsumerZone(), files)
	if !hcFindingsHave(got, "rogue") {
		t.Fatalf("expected direct typed Manager/Process rejection; got %v", got)
	}
}

func TestTask74Detector_RejectsDirectTypedAliasNestedGlobalFactoryCallback(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import (
	"context"
	rb "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	rh "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)
type mgrAlias = rh.Manager
type bag struct{ Mgr *rh.Manager; Proc *rb.ProcessServices }
type outer struct{ bag }
var globalMgr *rh.Manager
func newMgr() *rh.Manager { return nil }
func newProc() *rb.ProcessServices { return nil }
func viaAlias(ctx context.Context, m *mgrAlias) error { return m.ShutdownDetached(ctx) }
func viaNested(ctx context.Context, o *outer) error { return o.Mgr.ShutdownDetached(ctx) }
func viaGlobal(ctx context.Context) error { return globalMgr.ShutdownDetached(ctx) }
func viaFactory(ctx context.Context) error {
	_ = newMgr().ShutdownDetached(ctx)
	return newProc().Close()
}
func viaCallback(ctx context.Context, m *rh.Manager) error {
	fn := m.ShutdownDetached
	return fn(ctx)
}
func viaNew(ctx context.Context) error {
	m := new(rh.Manager)
	return m.ShutdownDetached(ctx)
}
`,
	})
	got := analyzeHostCloseOwnership(hcConsumerZone(), files)
	for _, want := range []string{"viaAlias", "viaNested", "viaGlobal", "viaFactory", "viaCallback", "viaNew"} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected %s rejection; got %v", want, got)
		}
	}
}

func TestTask74Detector_RejectsRogueTypedWorkflowInOwnerPackage(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl + hcPreHostOwnerDecl,
		"rogue.go": `
package runtimebundle
import "context"
func rogue(ctx context.Context, m *Manager, p *ProcessServices) error {
	_ = m.ShutdownDetached(ctx)
	return p.Close()
}
`,
	})
	got := analyzeHostCloseOwnership(hcOwnerZone(), files)
	if !hcFindingsHave(got, "rogue") {
		t.Fatalf("expected rogue typed workflow rejection in owner package; got %v", got)
	}
}

func TestTask74Detector_RejectsSecondPreHostRollbackOwner(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl + hcPreHostOwnerDecl,
		"extra.go": `
package runtimebundle
import "context"
func anotherPreHostRollback(ctx context.Context, primary error, genRollback func() error, processClose func() error, traceShutdown func(context.Context) error) error {
	if genRollback != nil { _ = genRollback() }
	if processClose != nil { _ = processClose() }
	if traceShutdown != nil { _ = traceShutdown(ctx) }
	return primary
}
`,
	})
	got := analyzeHostCloseOwnership(hcOwnerZone(), files)
	if !hcFindingsHave(got, "multiple pre-Host rollback owners") {
		t.Fatalf("expected second pre-Host owner rejection; got %v", got)
	}
}

func TestTask74Detector_RejectsDuplicateContestedDeclarations(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl,
		"dup.go": `
package runtimebundle
type Manager struct{}
type ProcessServices struct{}
type ReloadHost struct{}
`,
	})
	got := analyzeHostCloseOwnership(hcOwnerZone(), files)
	if !hcFindingsHave(got, "duplicate contested declaration") {
		t.Fatalf("expected duplicate contested declaration rejection; got %v", got)
	}
}

func TestTask74Detector_AcceptsCanonicalPreHostRollback(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl + hcPreHostOwnerDecl,
		"build.go": `
package runtimebundle
import "context"
func buildStage(ctx context.Context, mgr *Manager, ps *ProcessServices, trace func(context.Context) error) error {
	return joinInitialFailureCleanup(ctx, nil, func() error {
		return mgr.ShutdownDetached(ctx)
	}, ps.Close, trace)
}
type resource struct{}
func (r *resource) Close() error { return nil }
func disposeLocal(r *resource) error { return r.Close() }
`,
	})
	if got := analyzeHostCloseOwnership(hcOwnerZone(), files); len(got) != 0 {
		t.Fatalf("canonical pre-Host rollback and resource-local Close must pass; got %v", got)
	}
}

func TestTask74Detector_AcceptsUnrelatedShutdownNegatives(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import (
	"context"
	"database/sql"
	"net/http"
)
type mgmt interface{ Shutdown(context.Context) error }
type readOnly interface{ Status() string }
func drain(ctx context.Context, db *sql.DB, srv *http.Server, m mgmt, r readOnly) error {
	_ = db.Close()
	_ = r.Status()
	if err := srv.Shutdown(ctx); err != nil { return err }
	return m.Shutdown(ctx)
}
`,
	})
	if got := analyzeHostCloseOwnership(hcConsumerZone(), files); len(got) != 0 {
		t.Fatalf("unrelated db/http/management Shutdown must pass; got %v", got)
	}
}

func TestTask74Detector_RejectsHostDerivedPreHostFeeders(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"host.go": hcOwnerHostDecl + hcPreHostOwnerDecl,
		"rogue.go": `
package runtimebundle
import "context"
func rogueDirect(ctx context.Context, h *ReloadHost, err error) error {
	return joinInitialFailureCleanup(ctx, err,
		func() error { return h.Manager.ShutdownDetached(ctx) },
		h.Process.Close,
		h.ShutdownTracing,
	)
}
func rogueAlias(ctx context.Context, h *ReloadHost, err error) error {
	host := h
	return joinInitialFailureCleanup(ctx, err,
		func() error { return host.Manager.ShutdownDetached(ctx) },
		host.Process.Close,
		host.ShutdownTracing,
	)
}
type holder struct{ host *ReloadHost }
func rogueNested(ctx context.Context, box holder, err error) error {
	return joinInitialFailureCleanup(ctx, err,
		func() error { return box.host.Manager.ShutdownDetached(ctx) },
		box.host.Process.Close,
		box.host.ShutdownTracing,
	)
}
func newHost() *ReloadHost { return nil }
func rogueFactory(ctx context.Context, err error) error {
	h := newHost()
	return joinInitialFailureCleanup(ctx, err,
		func() error { return h.Manager.ShutdownDetached(ctx) },
		h.Process.Close,
		h.ShutdownTracing,
	)
}
var globalHost *ReloadHost
func rogueGlobal(ctx context.Context, err error) error {
	return joinInitialFailureCleanup(ctx, err,
		func() error { return globalHost.Manager.ShutdownDetached(ctx) },
		globalHost.Process.Close,
		globalHost.ShutdownTracing,
	)
}
func rogueCallback(ctx context.Context, h *ReloadHost, err error) error {
	rollback := func() error { return h.Manager.ShutdownDetached(ctx) }
	return joinInitialFailureCleanup(ctx, err, rollback, h.Process.Close, h.ShutdownTracing)
}
`,
	})
	got := analyzeHostCloseOwnership(hcOwnerZone(), files)
	for _, want := range []string{"rogueDirect", "rogueAlias", "rogueNested", "rogueFactory", "rogueGlobal", "rogueCallback"} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected %s Host-derived pre-Host feeder rejection; got %v", want, got)
		}
	}
}

func TestTask74Detector_RejectsStructuralInterfaceEquivalents(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import "context"
type stopper interface { ShutdownDetached(context.Context) error }
type shutdownCoordinator interface {
	BeginShutdown()
	WaitForIdle(context.Context) error
}
type processCloser interface {
	Close() error
	Closed() bool
}
type embeddedStopper interface { stopper }
type stopperAlias = stopper
type concreteMgr struct{}
func (concreteMgr) ShutdownDetached(context.Context) error { return nil }
type concreteProc struct{}
func (concreteProc) Close() error { return nil }
func (concreteProc) Closed() bool { return false }
type bag struct{ M stopper; P processCloser }
type outer struct{ bag }
var globalStopper stopper
func newStopper() stopper { return nil }
func newProc() processCloser { return nil }
func viaInterface(ctx context.Context, m stopper, p processCloser) error {
	_ = m.ShutdownDetached(ctx)
	return p.Close()
}
func viaEmbedded(ctx context.Context, m embeddedStopper) error { return m.ShutdownDetached(ctx) }
func viaAlias(ctx context.Context, m stopperAlias) error { return m.ShutdownDetached(ctx) }
func viaConcrete(ctx context.Context, m concreteMgr, p concreteProc) error {
	_ = m.ShutdownDetached(ctx)
	return p.Close()
}
func viaNested(ctx context.Context, o *outer) error { return o.M.ShutdownDetached(ctx) }
func viaGlobal(ctx context.Context) error { return globalStopper.ShutdownDetached(ctx) }
func viaFactory(ctx context.Context) error {
	_ = newStopper().ShutdownDetached(ctx)
	return newProc().Close()
}
func viaCallback(ctx context.Context, m stopper) error {
	fn := m.ShutdownDetached
	return fn(ctx)
}
func viaCoordinator(ctx context.Context, c shutdownCoordinator) error {
	c.BeginShutdown()
	return c.WaitForIdle(ctx)
}
`,
	})
	got := analyzeHostCloseOwnership(hcConsumerZone(), files)
	for _, want := range []string{
		"viaInterface", "viaEmbedded", "viaAlias", "viaConcrete", "viaNested",
		"viaGlobal", "viaFactory", "viaCallback", "viaCoordinator",
	} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected %s structural equivalent rejection; got %v", want, got)
		}
	}
}

func TestTask74Detector_AcceptsResourceOnlyAndReadOnlyInterfaces(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"serve.go": `
package main
import "context"
type closer interface { Close() error }
type mgmt interface { Shutdown(context.Context) error }
type statusOnly interface {
	Status() string
	FixedSourcePath() string
}
func drain(ctx context.Context, c closer, m mgmt, s statusOnly) error {
	_ = c.Close()
	_ = s.Status()
	_ = s.FixedSourcePath()
	return m.Shutdown(ctx)
}
`,
	})
	if got := analyzeHostCloseOwnership(hcConsumerZone(), files); len(got) != 0 {
		t.Fatalf("resource-only Close / management Shutdown / read-only interfaces must pass; got %v", got)
	}
}

// hcSamePackageCanonicalDecl declares exact canonical identities so fixtures can
// prove renamed structural equivalents beside them are still contested.
const hcSamePackageCanonicalDecl = `
package runtimehost
import "context"
type Manager struct{}
func (m *Manager) ShutdownDetached(ctx context.Context) error {
	m.BeginShutdown()
	return nil
}
func (m *Manager) BeginShutdown() {}
type Coordinator struct {
	gate *attemptGate
	mgr  *Manager
}
func (c *Coordinator) BeginShutdown() {
	if c.gate != nil { c.gate.BeginShutdown() }
	if c.mgr != nil { c.mgr.BeginShutdown() }
}
func (c *Coordinator) WaitForIdle(ctx context.Context) error {
	if c.gate == nil { return nil }
	return c.gate.WaitForIdle(ctx)
}
type attemptGate struct{}
func (g *attemptGate) BeginShutdown() {}
func (g *attemptGate) WaitForIdle(ctx context.Context) error { return nil }
type ProcessServices struct{ resource *resource }
func (p *ProcessServices) Close() error {
	if p.resource != nil { return p.resource.Close() }
	return nil
}
func (p *ProcessServices) Closed() bool { return false }
type resource struct{}
func (r *resource) Close() error { return nil }
`

func hcRuntimehostZone() hcZone { return hcZone{Scope: "internal/infra/runtimehost", HostOwner: false} }

func TestTask74Detector_RejectsSamePackageRenamedStructuralEquivalents(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"rogue.go": `
package runtimehost
import "context"
type shadowScheduler struct{}
func (*shadowScheduler) ShutdownDetached(context.Context) error { return nil }
type shadowSchedAlias = shadowScheduler
type shadowCoordinator struct{}
func (*shadowCoordinator) BeginShutdown() {}
func (*shadowCoordinator) WaitForIdle(context.Context) error { return nil }
type shadowProcess struct{}
func (*shadowProcess) Close() error { return nil }
func (*shadowProcess) Closed() bool { return false }
type mgrBag struct{ S *shadowScheduler }
type outerMgr struct{ mgrBag }
type coordBag struct{ C *shadowCoordinator }
type procBag struct{ P *shadowProcess }
var globalShadow *shadowScheduler
var globalCoord *shadowCoordinator
var globalProc *shadowProcess
func newShadow() *shadowScheduler { return nil }
func newShadowCoord() *shadowCoordinator { return nil }
func newShadowProc() *shadowProcess { return nil }
func rogueDirect(ctx context.Context, s *shadowScheduler) error { return s.ShutdownDetached(ctx) }
func rogueAlias(ctx context.Context, s *shadowSchedAlias) error { return s.ShutdownDetached(ctx) }
func rogueCoord(ctx context.Context, c *shadowCoordinator) error {
	c.BeginShutdown()
	return c.WaitForIdle(ctx)
}
func rogueProc(p *shadowProcess) error { return p.Close() }
func rogueNested(ctx context.Context, o *outerMgr) error { return o.S.ShutdownDetached(ctx) }
func rogueCoordNested(ctx context.Context, b *coordBag) error { return b.C.WaitForIdle(ctx) }
func rogueProcNested(b *procBag) error { return b.P.Close() }
func rogueGlobal(ctx context.Context) error { return globalShadow.ShutdownDetached(ctx) }
func rogueCoordGlobal(ctx context.Context) error { return globalCoord.WaitForIdle(ctx) }
func rogueProcGlobal() error { return globalProc.Close() }
func rogueFactory(ctx context.Context) error {
	_ = newShadow().ShutdownDetached(ctx)
	_ = newShadowCoord().WaitForIdle(ctx)
	return newShadowProc().Close()
}
func rogueCallback(ctx context.Context, s *shadowScheduler, c *shadowCoordinator, p *shadowProcess) error {
	fn := s.ShutdownDetached
	_ = fn(ctx)
	cfn := c.WaitForIdle
	_ = cfn(ctx)
	pfn := p.Close
	return pfn()
}
`,
	})
	got := analyzeHostCloseOwnership(hcRuntimehostZone(), files)
	for _, want := range []string{
		"rogueDirect", "rogueAlias", "rogueCoord", "rogueProc",
		"rogueNested", "rogueCoordNested", "rogueProcNested",
		"rogueGlobal", "rogueCoordGlobal", "rogueProcGlobal",
		"rogueFactory", "rogueCallback",
	} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected same-package renamed equivalent rejection for %s; got %v", want, got)
		}
	}
}

func TestTask74Detector_AcceptsCanonicalOwnerInternalHelpers(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"negatives.go": `
package runtimehost
import "context"
type wrongClosed struct{}
func (*wrongClosed) Close() error { return nil }
func (*wrongClosed) Closed() string { return "" }
type wrongCloseSig struct{}
func (*wrongCloseSig) Close(context.Context) error { return nil }
func (*wrongCloseSig) Closed() bool { return false }
type bareCloser struct{}
func (*bareCloser) Close() error { return nil }
func useWrong(ctx context.Context, a *wrongClosed, b *wrongCloseSig, c *bareCloser) error {
	_ = a.Close()
	_ = b.Close(ctx)
	return c.Close()
}
func disposeBare(c *bareCloser) error { return c.Close() }
`,
	})
	if got := analyzeHostCloseOwnership(hcRuntimehostZone(), files); len(got) != 0 {
		t.Fatalf("canonical Manager/Coordinator/ProcessServices helpers and unrelated closers must pass; got %v", got)
	}
}

func TestTask74Detector_RejectsPassiveRenamedStorageWithoutInvocation(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"passive.go": `
package runtimehost
import "context"
type shadowScheduler struct{}
func (*shadowScheduler) ShutdownDetached(context.Context) error { return nil }
type shadowSchedAlias = shadowScheduler
type shadowCoordinator struct{}
func (*shadowCoordinator) BeginShutdown() {}
func (*shadowCoordinator) WaitForIdle(context.Context) error { return nil }
type shadowProcess struct{}
func (*shadowProcess) Close() error { return nil }
func (*shadowProcess) Closed() bool { return false }
type rogueBag struct{ S *shadowScheduler }
type nestedBag struct{ rogueBag }
type coordBag struct{ C *shadowCoordinator }
type procBag struct{ P *shadowProcess }
type box struct{ Items []*shadowScheduler }
var globalShadow *shadowScheduler
var globalAlias *shadowSchedAlias
var globalCoord *shadowCoordinator
var globalProc *shadowProcess
var globalSlice []*shadowScheduler
var globalFactory = newPassiveShadow()
func newPassiveShadow() *shadowScheduler { return nil }
func newPassiveCoord() *shadowCoordinator { return nil }
func stash(s *shadowScheduler) { _ = s }
func passAlias(s *shadowSchedAlias) { stash(s) }
func returnShadow() *shadowScheduler { return globalShadow }
func returnFactory() *shadowScheduler { return newPassiveShadow() }
func assignLocal() {
	var local *shadowScheduler
	local = newPassiveShadow()
	_ = local
}
func registerCB(fn func(context.Context) error) {}
func callback(s *shadowScheduler) { registerCB(s.ShutdownDetached) }
`,
	})
	got := analyzeHostCloseOwnership(hcRuntimehostZone(), files)
	for _, want := range []string{
		"globalShadow", "globalAlias", "globalCoord", "globalProc", "globalSlice", "globalFactory",
		"rogueBag", "nestedBag", "coordBag", "procBag", "box",
		"stash", "passAlias", "returnShadow", "returnFactory", "assignLocal", "callback",
	} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected passive renamed-storage rejection for %s; got %v", want, got)
		}
	}
}

func TestTask74Detector_AcceptsExactCanonicalWiringNegatives(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"wire.go": `
package runtimehost
import "context"
var sharedManager *Manager
func NewCoordinatorWire(mgr *Manager) *Coordinator {
	gate := &attemptGate{}
	return &Coordinator{gate: gate, mgr: mgr}
}
func (c *Coordinator) rebind(mgr *Manager) {
	c.mgr = mgr
}
func newProcess(resource *resource) *ProcessServices {
	return &ProcessServices{resource: resource}
}
`,
	})
	if got := analyzeHostCloseOwnership(hcRuntimehostZone(), files); len(got) != 0 {
		t.Fatalf("exact canonical field/global/constructor wiring must pass; got %v", got)
	}
}

func TestTask74Detector_RejectsCanonicalMethodRogueShutdown(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"rogue.go": `
package runtimehost
import "context"
type shadowScheduler struct{}
func (*shadowScheduler) ShutdownDetached(context.Context) error { return nil }
func (m *Manager) Rogue(ctx context.Context, s *shadowScheduler) error {
	return s.ShutdownDetached(ctx)
}
func (c *Coordinator) Rogue(ctx context.Context, s *shadowScheduler) error {
	return s.ShutdownDetached(ctx)
}
func (c *Coordinator) RogueDirect(ctx context.Context, m *Manager) error {
	return m.ShutdownDetached(ctx)
}
`,
	})
	got := analyzeHostCloseOwnership(hcRuntimehostZone(), files)
	for _, want := range []string{"Manager.Rogue", "Coordinator.Rogue", "Coordinator.RogueDirect"} {
		if !hcFindingsHave(got, want) {
			t.Fatalf("expected canonical-method rogue rejection for %s; got %v", want, got)
		}
	}
	// Shutdown roots remain blessed/accepted.
	for _, root := range []string{"Manager.ShutdownDetached", "Coordinator.BeginShutdown", "Coordinator.WaitForIdle"} {
		if hcFindingsHave(got, root+"|") || hcFindingsHave(got, root+":") {
			t.Fatalf("canonical shutdown root %s must remain accepted; got %v", root, got)
		}
	}
}

func TestTask74Detector_StructuralMutationEvidence(t *testing.T) {
	t.Parallel()
	pos := analyzeHostCloseOwnership(hcRuntimehostZone(), mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"mut.go": `
package runtimehost
type almostProcess struct{}
func (*almostProcess) Close() error { return nil }
func (*almostProcess) Closed() bool { return false }
func rogueAlmost(p *almostProcess) error { return p.Close() }
`,
	}))
	if !hcFindingsHave(pos, "rogueAlmost") {
		t.Fatalf("adding Closed() must make process-shaped rogue fail; got %v", pos)
	}
	neg := analyzeHostCloseOwnership(hcRuntimehostZone(), mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"mut.go": `
package runtimehost
type almostProcess struct{}
func (*almostProcess) Close() error { return nil }
func rogueAlmost(p *almostProcess) error { return p.Close() }
`,
	}))
	if hcFindingsHave(neg, "rogueAlmost") || len(neg) != 0 {
		t.Fatalf("removing Closed() must leave bare Close as accepted negative; got %v", neg)
	}

	mgrPos := analyzeHostCloseOwnership(hcRuntimehostZone(), mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"mut.go": `
package runtimehost
import "context"
type almostMgr struct{}
func (*almostMgr) ShutdownDetached(context.Context) error { return nil }
func rogueAlmostMgr(ctx context.Context, m *almostMgr) error { return m.ShutdownDetached(ctx) }
`,
	}))
	if !hcFindingsHave(mgrPos, "rogueAlmostMgr") {
		t.Fatalf("ShutdownDetached must make manager-shaped rogue fail; got %v", mgrPos)
	}
	mgrNeg := analyzeHostCloseOwnership(hcRuntimehostZone(), mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"mut.go": `
package runtimehost
import "context"
type almostMgr struct{}
func (*almostMgr) Shutdown(context.Context) error { return nil }
func rogueAlmostMgr(ctx context.Context, m *almostMgr) error { return m.Shutdown(ctx) }
`,
	}))
	if hcFindingsHave(mgrNeg, "rogueAlmostMgr") || len(mgrNeg) != 0 {
		t.Fatalf("renaming away ShutdownDetached must accept unrelated negative; got %v", mgrNeg)
	}

	coordPos := analyzeHostCloseOwnership(hcRuntimehostZone(), mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"mut.go": `
package runtimehost
import "context"
type almostCoord struct{}
func (*almostCoord) BeginShutdown() {}
func (*almostCoord) WaitForIdle(context.Context) error { return nil }
func rogueAlmostCoord(ctx context.Context, c *almostCoord) error {
	c.BeginShutdown()
	return c.WaitForIdle(ctx)
}
`,
	}))
	if !hcFindingsHave(coordPos, "rogueAlmostCoord") {
		t.Fatalf("BeginShutdown+WaitForIdle must make coordinator-shaped rogue fail; got %v", coordPos)
	}
	coordNeg := analyzeHostCloseOwnership(hcRuntimehostZone(), mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"mut.go": `
package runtimehost
import "context"
type almostCoord struct{}
func (*almostCoord) BeginShutdown() {}
func rogueAlmostCoord(ctx context.Context, c *almostCoord) error {
	c.BeginShutdown()
	return nil
}
`,
	}))
	if hcFindingsHave(coordNeg, "rogueAlmostCoord") || len(coordNeg) != 0 {
		t.Fatalf("removing WaitForIdle must accept unrelated negative; got %v", coordNeg)
	}
}

func TestTask74Detector_RejectsDuplicateRenamedEquivalentMethods(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"canonical.go": hcSamePackageCanonicalDecl,
		"dup.go": `
package runtimehost
import "context"
type shadowDup struct{}
func (*shadowDup) ShutdownDetached(context.Context) error { return nil }
func (*shadowDup) ShutdownDetached(context.Context) error { return nil }
func rogueDup(ctx context.Context, s *shadowDup) error { return s.ShutdownDetached(ctx) }
`,
	})
	got := analyzeHostCloseOwnership(hcRuntimehostZone(), files)
	if !hcFindingsHave(got, "duplicate contested declaration") {
		t.Fatalf("expected duplicate renamed-equivalent method rejection; got %v", got)
	}
}

// --- serve input shape fixtures ---

const hcServeEntry = `
func RunWithGenerationHost(ctx context.Context, in GenerationHostInput) error { return nil }
`

func TestTask74Detector_ServeInputCanonicalShapeAccepted(t *testing.T) {
	t.Parallel()
	files := mustParseHCFiles(t, map[string]string{
		"generation_host.go": `
package stdhttp
import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)
type GenerationServeHost interface {
	HTTPHandler() http.Handler
	BeginShutdown()
	Close(context.Context) error
}
type GenerationHostInput struct {
	Config *config.Config
	Log *slog.Logger
	Host GenerationServeHost
	Management interface{ Shutdown(context.Context) error }
	ShutdownTimeout time.Duration
}
` + hcServeEntry,
	})
	if got := analyzeServeInputShape(files); len(got) != 0 {
		t.Fatalf("canonical serve input must pass; got %v", got)
	}
}

func TestTask74Detector_ServeInputRejectsOwnershipFields(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"manager": `	Manager *runtimehost.Manager
`,
		"process": `	Process *runtimebundle.ProcessServices
`,
		"coordinator_bag": `	Coordinator interface{ BeginShutdown(); WaitForIdle(context.Context) error }
`,
		"tracing_callback": `	ShutdownTracing func(context.Context) error
`,
		"nested_bag": `	Lifecycle lifecycleBag
`,
		"renamed_host_superset": `	Plane interface{ HTTPHandler() http.Handler; BeginShutdown(); WaitForIdle(context.Context) error; Close(context.Context) error }
`,
	}
	for name, field := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := mustParseHCFiles(t, map[string]string{
				"generation_host.go": `
package stdhttp
import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)
type lifecycleBag struct{ Close func() error }
type GenerationServeHost interface {
	HTTPHandler() http.Handler
	BeginShutdown()
	Close(context.Context) error
}
type GenerationHostInput struct {
	Config *config.Config
	Log *slog.Logger
	Host GenerationServeHost
	ShutdownTimeout time.Duration
` + field + `}
` + hcServeEntry,
			})
			if got := analyzeServeInputShape(files); len(got) == 0 {
				t.Fatalf("serve input field %s must be rejected", name)
			}
		})
	}
}
