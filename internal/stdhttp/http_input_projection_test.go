package stdhttp

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"
)

// TestStandardHTTPInputFromBuilt_mapsInventoryFieldsOnce proves Built→group
// projection maps every mount-inventory capability exactly once and omits
// lifecycle/ownership fields (Closers, host, ledger).
func TestStandardHTTPInputFromBuilt_mapsInventoryFieldsOnce(t *testing.T) {
	t.Parallel()
	exec := runtime.TestExecutor()
	reg := pluginreg.NewRegistry()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	metricsBundle := &metrics.Bundle{Registry: prometheus.NewRegistry()}
	secretGuard := &diag.InventoryExtras{SecretGuardAccessMode: "deny"}
	cpQueries := &controlplane.QueryService{}
	readiness := &controlplane.ReadinessReportService{}
	tokenAdmin := &accountingapp.Service{}
	usage := &authorityapp.Service{}
	concurrency := &concurrencyapp.Service{}
	catalog := &modelcatalog.CatalogRuntime{}
	modelRT := &modelregistry.Runtime{}
	providers := []httpauth.Provider{rejectAllAuthProvider{}}
	snap := &extensions.RequestRuntimeSnapshot{}
	decode := lipsdk.DecodeAdmission(nil)
	regs := []lipsdk.Registration{{ID: "feat-a"}}

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
		},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 12345,
			PreRequestKeepalive: config.PreRequestKeepaliveConfig{Enabled: true, Interval: "1s"},
		},
	}
	built := &runtimebundle.Built{
		Executor:              exec,
		Store:                 store,
		Closers:               []func() error{func() error { return nil }},
		EffectiveDefaultRoute: "stub:route",
		RoutePrefixes:         []string{"stub"},
		DecodeAdmission:       decode,
		PluginRegistry:        reg,
		Metrics:               metricsBundle,
		RuntimeSnapshot:       snap,
		HTTPAuthProviders:     providers,
		SecureSessionStore:    nil,
		CatalogRuntime:        catalog,
		ModelRegistryRuntime:  modelRT,
		TokenAccountingAdmin:  tokenAdmin,
		ControlPlaneQueries:   cpQueries,
		UsageAuthority:        usage,
		ConcurrencyAuthority:  concurrency,
		ReadinessReport:       readiness,
		SecretGuardInventory:  secretGuard,
	}

	got := standardHTTPInputFromBuilt(built, cfg, regs)

	// Core
	if got.Core.Executor != exec {
		t.Fatal("Core.Executor not projected")
	}
	// Security
	if len(got.Security.HTTPAuthProviders) != 1 || got.Security.UsageAuthority != usage || got.Security.ConcurrencyAuthority != concurrency {
		t.Fatalf("Security projection incomplete: %+v", got.Security)
	}
	if got.Security.SecureSessionStore != nil {
		t.Fatal("Security.SecureSessionStore not projected")
	}
	// Operations
	ops := got.Operations
	if ops.Metrics != metricsBundle || ops.Store != store || ops.SecretGuardInventory != secretGuard {
		t.Fatal("Operations metrics/store/secret-guard not projected")
	}
	if ops.ControlPlaneQueries != cpQueries || ops.ReadinessReport != readiness {
		t.Fatal("Operations query surfaces not projected")
	}
	if ops.TokenAccountingAdmin == nil {
		t.Fatal("Operations.TokenAccountingAdmin must be adapted from Built")
	}
	if len(ops.Registrations) != 1 || ops.Registrations[0].ID != "feat-a" {
		t.Fatalf("Operations.Registrations=%v", ops.Registrations)
	}
	// Models
	if got.Models.CatalogRuntime != catalog || got.Models.ModelRegistryRuntime != modelRT {
		t.Fatal("Models not projected")
	}
	// Frontends
	fe := got.Frontends
	if fe.Executor != exec || fe.Registry != reg || fe.DefaultRouteSelector != "stub:route" {
		t.Fatal("Frontends executor/registry/route not projected")
	}
	if len(fe.RoutePrefixes) != 1 || fe.RoutePrefixes[0] != "stub" {
		t.Fatalf("Frontends.RoutePrefixes=%v", fe.RoutePrefixes)
	}
	if fe.MaxRequestBodyBytes != cfg.Server.EffectiveMaxRequestBodyBytes() {
		t.Fatalf("MaxRequestBodyBytes=%d", fe.MaxRequestBodyBytes)
	}
	if !fe.PreRequestKeepalive.Enabled || fe.PreRequestKeepalive.Interval != time.Second {
		t.Fatalf("PreRequestKeepalive=%+v", fe.PreRequestKeepalive)
	}
	if len(fe.Plugins) != 1 || fe.Plugins[0].ID != "openai-responses" {
		t.Fatalf("Plugins=%v", fe.Plugins)
	}

	// Lifecycle fields must not appear on any group (compile-time shape + runtime absence).
	assertNoLifecycleFieldsOnGroups(t)
}

func TestStandardHTTPInputFromBuilt_nilBuiltAndOptionalCapabilities(t *testing.T) {
	t.Parallel()
	got := standardHTTPInputFromBuilt(nil, &config.Config{}, nil)
	if got.Core.Executor != nil || got.Operations.Metrics != nil || got.Models.CatalogRuntime != nil {
		t.Fatalf("nil Built must project zero optional capabilities, got %+v", got)
	}
	// Empty EffectiveDefaultRoute falls back to DefaultRouteSelector(cfg).
	got2 := standardHTTPInputFromBuilt(&runtimebundle.Built{}, &config.Config{
		Routing: config.RoutingConfig{DefaultRoute: "stub:fallback"},
	}, nil)
	if got2.Frontends.DefaultRouteSelector == "" {
		t.Fatal("empty Built route must fall back via DefaultRouteSelector")
	}
}

func TestStandardHTTPInputFromBuilt_defensiveClones(t *testing.T) {
	t.Parallel()
	providers := []httpauth.Provider{rejectAllAuthProvider{}}
	prefixes := []string{"stub"}
	var pluginYAML yaml.Node
	if err := yaml.Unmarshal([]byte("key: original\n"), &pluginYAML); err != nil {
		t.Fatal(err)
	}
	for pluginYAML.Kind == yaml.DocumentNode && len(pluginYAML.Content) > 0 {
		pluginYAML = *pluginYAML.Content[0]
	}
	plugins := []config.PluginConfig{{ID: "openai-responses", Enabled: true, Config: pluginYAML}}
	regs := []lipsdk.Registration{{ID: "feat-a", Config: lipsdk.ConfigPayload{Node: pluginYAML}}}
	redA := stubProjectionRedactor{id: "red-a"}
	reds := []traffic.Redactor{redA}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		TrafficRedactors: reds,
	})
	cfg := &config.Config{Plugins: config.PluginsConfig{Frontends: plugins}}
	built := &runtimebundle.Built{
		HTTPAuthProviders: providers,
		RoutePrefixes:     prefixes,
		RuntimeSnapshot:   snap,
	}

	got := standardHTTPInputFromBuilt(built, cfg, regs)
	got2 := standardHTTPInputFromBuilt(built, cfg, regs)

	// Mutate sources.
	providers[0] = nil
	prefixes[0] = "mutated"
	plugins[0].ID = "mutated"
	if len(pluginYAML.Content) >= 2 {
		pluginYAML.Content[1].Value = "mutated"
	}
	regs[0].ID = "mutated"
	reds[0] = stubProjectionRedactor{id: "mutated"}

	if got.Security.HTTPAuthProviders[0] == nil {
		t.Fatal("projected auth providers aliased source slice")
	}
	if got.Frontends.RoutePrefixes[0] != "stub" {
		t.Fatalf("projected RoutePrefixes=%v after source mutate", got.Frontends.RoutePrefixes)
	}
	if got.Frontends.Plugins[0].ID != "openai-responses" {
		t.Fatalf("projected Plugins aliased source: %v", got.Frontends.Plugins[0].ID)
	}
	if got.Operations.Registrations[0].ID != "feat-a" {
		t.Fatalf("projected Registrations aliased source: %v", got.Operations.Registrations[0].ID)
	}
	if len(got.Frontends.TrafficPorts.Red) != 1 || got.Frontends.TrafficPorts.Red[0].ID() != "red-a" {
		t.Fatalf("projected redactors=%v", got.Frontends.TrafficPorts.Red)
	}

	// Mutate projected → source integrity.
	got.Security.HTTPAuthProviders[0] = nil
	got.Frontends.RoutePrefixes[0] = "projected-mut"
	got.Frontends.Plugins[0].ID = "projected-mut"
	got.Operations.Registrations[0].ID = "projected-mut"
	got.Frontends.TrafficPorts.Red[0] = stubProjectionRedactor{id: "projected-mut"}

	if built.RoutePrefixes[0] != "mutated" && built.RoutePrefixes[0] != "stub" {
		// source was mutated earlier to "mutated"; projected mutation must not change it further.
		t.Fatalf("unexpected built prefixes %v", built.RoutePrefixes)
	}
	if built.RoutePrefixes[0] == "projected-mut" {
		t.Fatal("projected RoutePrefixes mutation leaked into Built")
	}
	if cfg.Plugins.Frontends[0].ID == "projected-mut" {
		t.Fatal("projected Plugins mutation leaked into cfg")
	}
	if regs[0].ID == "projected-mut" {
		t.Fatal("projected Registrations mutation leaked into source regs")
	}

	// Repeated projection must not share mutable backing arrays.
	if len(got.Frontends.RoutePrefixes) > 0 && len(got2.Frontends.RoutePrefixes) > 0 {
		got.Frontends.RoutePrefixes[0] = "only-got"
		if got2.Frontends.RoutePrefixes[0] == "only-got" {
			t.Fatal("repeated Built projections share RoutePrefixes backing array")
		}
	}
	if len(got.Frontends.TrafficPorts.Red) > 0 && len(got2.Frontends.TrafficPorts.Red) > 0 {
		got.Frontends.TrafficPorts.Red[0] = stubProjectionRedactor{id: "only-got"}
		if got2.Frontends.TrafficPorts.Red[0].ID() == "only-got" {
			t.Fatal("repeated Built projections share TrafficPorts.Red backing array")
		}
	}
}

func TestStandardHTTPInputFromRequestPlane_defensiveClones(t *testing.T) {
	t.Parallel()
	ps := newProjectionProcess(t)
	frontends := []config.PluginConfig{{ID: "openai-responses", Enabled: true}}
	cfg := stubProjectionConfig(t, "proj", "ok", "proj:stub-default", frontends)
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{Process: ps, Candidate: cfg})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	captured := runtimebundle.NewCompatRequestPlane(cand, cfg, testkit.DiscardLogger(), config.RegistrationsFromConfig(cfg), runtimebundle.FrozenRoutingView{DefaultRoute: cand.EffectiveDefaultRoute, RoutePrefixes: cand.RoutePrefixes})
	got := standardHTTPInputFromRequestPlane(captured)
	got2 := standardHTTPInputFromRequestPlane(captured)

	if len(got.Frontends.Plugins) == 0 {
		t.Fatal("expected projected frontends")
	}
	origPluginID := got.Frontends.Plugins[0].ID
	origPrefixes := append([]string(nil), got.Frontends.RoutePrefixes...)
	origRegs := append([]lipsdk.Registration(nil), got.Operations.Registrations...)
	origAuthLen := len(got.Security.HTTPAuthProviders)

	// Mutate projected collections.
	got.Frontends.Plugins[0].ID = "projected-mut"
	if len(got.Frontends.RoutePrefixes) > 0 {
		got.Frontends.RoutePrefixes[0] = "projected-mut"
	}
	if len(got.Operations.Registrations) > 0 {
		got.Operations.Registrations[0].ID = "projected-mut"
	}
	if len(got.Security.HTTPAuthProviders) > 0 {
		got.Security.HTTPAuthProviders[0] = nil
	}
	if len(got.Frontends.TrafficPorts.Red) > 0 {
		got.Frontends.TrafficPorts.Red[0] = stubProjectionRedactor{id: "projected-mut"}
	}

	// Source plane getters must remain unchanged.
	againPlugins := captured.Frontends()
	if len(againPlugins) == 0 || againPlugins[0].ID != origPluginID {
		t.Fatalf("RequestPlane Frontends mutated via projection: %v", againPlugins)
	}
	againRoute := captured.Routing()
	if len(origPrefixes) > 0 && (len(againRoute.RoutePrefixes) == 0 || againRoute.RoutePrefixes[0] == "projected-mut") {
		t.Fatalf("RequestPlane RoutePrefixes mutated via projection: %v", againRoute.RoutePrefixes)
	}
	againRegs := captured.Registrations()
	if len(origRegs) > 0 && (len(againRegs) == 0 || againRegs[0].ID == "projected-mut") {
		t.Fatalf("RequestPlane Registrations mutated via projection: %v", againRegs)
	}
	againAuth := captured.HTTPAuthProviders()
	if origAuthLen > 0 && len(againAuth) != origAuthLen {
		t.Fatalf("RequestPlane HTTPAuthProviders length changed: %d", len(againAuth))
	}

	// Mutate plane getter copies / stack config clone and ensure prior projection stable.
	stack := captured.StackConfig()
	if stack != nil && len(stack.Plugins.Frontends) > 0 {
		stack.Plugins.Frontends[0].ID = "stack-mut"
	}
	getterPlugins := captured.Frontends()
	if len(getterPlugins) > 0 {
		getterPlugins[0].ID = "getter-mut"
	}
	if got2.Frontends.Plugins[0].ID != origPluginID {
		t.Fatalf("second projection contaminated: %s", got2.Frontends.Plugins[0].ID)
	}

	// Repeated projection must not share mutable backing arrays.
	if len(got.Frontends.RoutePrefixes) > 0 && len(got2.Frontends.RoutePrefixes) > 0 {
		got.Frontends.RoutePrefixes[0] = "only-got"
		if got2.Frontends.RoutePrefixes[0] == "only-got" {
			t.Fatal("repeated RequestPlane projections share RoutePrefixes backing array")
		}
	}
	if len(got.Operations.Registrations) > 0 && len(got2.Operations.Registrations) > 0 {
		got.Operations.Registrations[0].ID = "only-got"
		if got2.Operations.Registrations[0].ID == "only-got" {
			t.Fatal("repeated RequestPlane projections share Registrations backing array")
		}
	}
	if len(got.Frontends.TrafficPorts.Red) > 0 && len(got2.Frontends.TrafficPorts.Red) > 0 {
		got.Frontends.TrafficPorts.Red[0] = stubProjectionRedactor{id: "only-got"}
		if got2.Frontends.TrafficPorts.Red[0].ID() == "only-got" {
			t.Fatal("repeated RequestPlane projections share TrafficPorts.Red backing array")
		}
	}
}

func TestTrafficPortsFromSnapshot_clonesRedactors(t *testing.T) {
	t.Parallel()
	reds := []traffic.Redactor{stubProjectionRedactor{id: "a"}}
	snap := fakeTrafficSnapshot{red: reds}
	ports := trafficPortsFromSnapshot(snap)
	reds[0] = stubProjectionRedactor{id: "mutated"}
	if ports.Red[0].ID() != "a" {
		t.Fatal("trafficPortsFromSnapshot aliased redactor slice")
	}
	ports.Red[0] = stubProjectionRedactor{id: "projected"}
	if reds[0].ID() == "projected" {
		t.Fatal("projected redactor mutation leaked into source")
	}
	ports2 := trafficPortsFromSnapshot(snap)
	ports.Red[0] = stubProjectionRedactor{id: "only-1"}
	if ports2.Red[0].ID() == "only-1" {
		t.Fatal("repeated trafficPortsFromSnapshot share redactor backing array")
	}
}

func assertNoLifecycleFieldsOnGroups(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Group definitions live in the cycle-neutral contract package (task 3.4);
	// root stdhttp only holds aliases.
	contractPath := filepath.Join(dir, "contract", "http_input.go")
	src, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "contract/http_input.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	prohibited := map[string]bool{
		"Closers": true, "Closer": true, "Built": true, "RequestPlane": true,
		"Host": true, "Coordinator": true, "ResourceLedger": true, "Ledger": true,
		"OnClose": true, "OnShutdown": true, "App": true,
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			switch ts.Name.Name {
			case "HTTPCoreInput", "HTTPSecurityInput", "HTTPOperationsInput", "HTTPModelInput", "HTTPFrontendInput", "StandardHTTPInput":
			default:
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, n := range field.Names {
					if prohibited[n.Name] {
						t.Fatalf("%s.%s is prohibited lifecycle/broad field", ts.Name.Name, n.Name)
					}
					if typeExprIsAny(field.Type) {
						t.Fatalf("%s.%s must not be any/interface{}", ts.Name.Name, n.Name)
					}
				}
			}
		}
	}
}

func typeExprIsAny(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "any"
	case *ast.InterfaceType:
		return t.Methods == nil || len(t.Methods.List) == 0
	case *ast.StarExpr:
		return typeExprIsAny(t.X)
	default:
		return false
	}
}

// TestTransitionalAdapters_projectToFocusedComposer is an AST proof that
// NewStandardHandler / RunWithRuntime / ComposeRequestPlane project into
// StandardHTTPInput and invoke prepareStandardHandler, while strict mounts are
// never called with Built/RequestPlane and requestPlaneAsBuilt is gone.
func TestTransitionalAdapters_projectToFocusedComposer(t *testing.T) {
	t.Parallel()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"handler.go":       mustReadFile(t, filepath.Join(dir, "handler.go")),
		"server.go":        mustReadFile(t, filepath.Join(dir, "server.go")),
		"request_plane.go": mustReadFile(t, filepath.Join(dir, "request_plane.go")),
	}
	for name, src := range files {
		if strings.Contains(src, "requestPlaneAsBuilt") {
			t.Fatalf("%s must not reference requestPlaneAsBuilt after Task 3.2", name)
		}
	}
	assertFuncCalls(t, files["handler.go"], "NewStandardHandler", []string{
		"standardHTTPInputFromBuilt", "prepareStandardHandler",
	})
	assertFuncCalls(t, files["server.go"], "RunWithRuntime", []string{
		"standardHTTPInputFromBuilt", "prepareStandardHandler",
	})
	assertFuncCalls(t, files["request_plane.go"], "ComposeRequestPlane", []string{
		"standardHTTPInputFromRequestPlane", "prepareStandardHandler",
	})

	// Strict mounts / prepareStandardHandler must not accept Built param.
	for _, name := range []string{
		"mount_metrics.go", "mount_diagnostics.go", "mount_admin.go",
		"mount_securesession.go", "middleware.go", "mount.go", "cancel.go", "handler.go",
	} {
		src := mustReadFile(t, filepath.Join(dir, name))
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Recv != nil {
				continue
			}
			switch fd.Name.Name {
			case "mountMetrics", "mountDiagnostics", "mountModelCatalogDiagnostics",
				"mountModelInventoryDiagnostics", "mountSecureSessionDiagnostics",
				"mountAccountingAdmin", "mountControlPlaneQuery", "mountAccountingAuthorityQuery",
				"MountBundledFrontends", "MountBundledFrontendsLegacy", "mountALegCancel",
				"stackHTTPHandler", "prepareStandardHandler":
			default:
				continue
			}
			if fd.Type == nil || fd.Type.Params == nil {
				continue
			}
			for _, p := range fd.Type.Params.List {
				if typeExprMentions(p.Type, "Built") || typeExprMentions(p.Type, "RequestPlane") {
					t.Fatalf("%s(%s) still accepts Built/RequestPlane", name, fd.Name.Name)
				}
			}
		}
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertFuncCalls(t *testing.T, src, funcName string, wantCalls []string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != funcName {
			continue
		}
		body = fd.Body
		break
	}
	if body == nil {
		t.Fatalf("function %s not found", funcName)
	}
	found := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := ce.Fun.(type) {
		case *ast.Ident:
			found[fun.Name] = true
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok {
				found[id.Name+"."+fun.Sel.Name] = true
			}
			found[fun.Sel.Name] = true
		}
		return true
	})
	for _, want := range wantCalls {
		if !found[want] {
			t.Fatalf("%s must call %s; found=%v", funcName, want, keysOf(found))
		}
	}
}

func typeExprMentions(expr ast.Expr, name string) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == name
	case *ast.StarExpr:
		return typeExprMentions(t.X, name)
	case *ast.SelectorExpr:
		return t.Sel != nil && t.Sel.Name == name
	case *ast.ArrayType:
		return typeExprMentions(t.Elt, name)
	default:
		return false
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

type stubProjectionRedactor struct{ id string }

func (s stubProjectionRedactor) ID() string { return s.id }
func (s stubProjectionRedactor) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type fakeTrafficSnapshot struct {
	red []traffic.Redactor
}

func (f fakeTrafficSnapshot) RawCapture() traffic.RawCaptureSink { return nil }
func (f fakeTrafficSnapshot) TrafficObserver() traffic.Observer  { return nil }
func (f fakeTrafficSnapshot) TrafficRedactors() []traffic.Redactor {
	return f.red
}

func newProjectionProcess(t *testing.T) *runtimebundle.ProcessServices {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func stubProjectionConfig(t *testing.T, backendID, text, defaultRoute string, frontends []config.PluginConfig) *config.Config {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("text: \""+text+"\"\ninput_tokens: 1\noutput_tokens: 1\n"), &n); err != nil {
		t.Fatal(err)
	}
	for n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = *n.Content[0]
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: defaultRoute},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Frontends: frontends,
			Backends: []config.PluginConfig{{
				Kind: "local-stub", ID: backendID, Enabled: true, Config: n,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return cfg
}

// Ensure typed adminaccounting.Service is what Operations holds (compile-time).
var _ adminaccounting.Service = adminaccounting.AdaptCountCallService((*accountingapp.Service)(nil))

func TestStandardHTTPInputFromBuilt_typedNilAuthoritiesProjectToNil(t *testing.T) {
	t.Parallel()
	var typedUsage *authorityapp.Service
	var typedConcurrency *concurrencyapp.Service
	var typedCP *controlplane.QueryService
	var typedReady *controlplane.ReadinessReportService
	var typedToken *accountingapp.Service
	built := &runtimebundle.Built{
		UsageAuthority:       typedUsage,
		ConcurrencyAuthority: typedConcurrency,
		ControlPlaneQueries:  typedCP,
		ReadinessReport:      typedReady,
		TokenAccountingAdmin: typedToken,
	}
	got := standardHTTPInputFromBuilt(built, &config.Config{}, nil)
	if got.Security.UsageAuthority != nil {
		t.Fatal("typed-nil UsageAuthority must project to nil interface")
	}
	if got.Security.ConcurrencyAuthority != nil {
		t.Fatal("typed-nil ConcurrencyAuthority must project to nil interface")
	}
	if got.Operations.ControlPlaneQueries != nil {
		t.Fatal("typed-nil ControlPlaneQueries must project to nil interface")
	}
	if got.Operations.ReadinessReport != nil {
		t.Fatal("typed-nil ReadinessReport must project to nil interface")
	}
	if got.Operations.TokenAccountingAdmin != nil {
		t.Fatal("typed-nil TokenAccountingAdmin must project to nil interface")
	}
}

func TestStandardHTTPInputFromRequestPlane_nilAuthoritiesProjectToNil(t *testing.T) {
	t.Parallel()
	ps := newProjectionProcess(t)
	cfg := stubProjectionConfig(t, "proj-auth", "ok", "proj-auth:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{Process: ps, Candidate: cfg})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	// Concrete nil pointers from plane getters must adapt to true nil interfaces.
	plane := runtimebundle.NewCompatRequestPlane(cand, cfg, testkit.DiscardLogger(), config.RegistrationsFromConfig(cfg), runtimebundle.FrozenRoutingView{DefaultRoute: cand.EffectiveDefaultRoute, RoutePrefixes: cand.RoutePrefixes})
	got := standardHTTPInputFromRequestPlane(plane)
	if got.Security.UsageAuthority != nil {
		t.Fatal("RequestPlane nil UsageAuthority must project to nil interface")
	}
	if got.Security.ConcurrencyAuthority != nil {
		t.Fatal("RequestPlane nil ConcurrencyAuthority must project to nil interface")
	}
	if got.Operations.TokenAccountingAdmin != nil {
		t.Fatal("RequestPlane nil TokenAccountingAdmin must project to nil interface")
	}
}

func TestStandardHTTPInputFromBuilt_typedNilAuthoritiesLeaveOptionalRoutesDisabled(t *testing.T) {
	t.Parallel()
	var typedUsage *authorityapp.Service
	var typedConcurrency *concurrencyapp.Service
	built := &runtimebundle.Built{
		Executor:             runtime.TestExecutor(),
		PluginRegistry:       pluginreg.NewRegistry(),
		UsageAuthority:       typedUsage,
		ConcurrencyAuthority: typedConcurrency,
	}
	got := standardHTTPInputFromBuilt(built, &config.Config{}, nil)
	if got.Security.UsageAuthority != nil || got.Security.ConcurrencyAuthority != nil {
		t.Fatal("typed-nil authorities must remain nil after projection")
	}
	// Config that would mount authority routes if UsageAuthority were non-nil.
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{SharedSecret: "test-secret"},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled: true,
				Query: config.AccountingAuthorityQueryConfig{
					Enabled:    true,
					PathPrefix: "/authority",
				},
			},
		},
	}
	mux := http.NewServeMux()
	mountAccountingAuthorityQuery(accountingAuthorityQueryMount{
		LogCtx:   context.Background(),
		Mux:      mux,
		Cfg:      cfg,
		Log:      nil,
		Core:     got.Core,
		Security: got.Security,
	})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/authority/status", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (route unmounted for typed-nil authority)", rr.Code)
	}
}
