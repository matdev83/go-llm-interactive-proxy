package archtest

import "testing"

func TestTask43Detector_SoleServeAPI_FlagsRunWithRuntimeAndExtras(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
func RunWithGenerationHost() error { return nil }
func RunWithRuntime() error { return nil }
func RunWithLegacyServe() error { return nil }
func Serve(ctx context.Context) error { return nil }
func ServeDataPlane() error { return nil }
func StartHTTPServer() error { return nil }
func StartServer() error { return nil }
func RunServer() error { return nil }
func ListenAndServeHTTP() error { return nil }
func prepareStandardHandler() {}
func ComposeStandardHTTP() {}
func MountFrontends() {}
func (h *Handler) ServeHTTP() {}
`
	got, err := scanTask43SoleServeAPISource("internal/stdhttp/sneak_serve.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"func:RunWithGenerationHost", "func:RunWithRuntime", "func:RunWithLegacyServe",
		"func:Serve", "func:ServeDataPlane", "func:StartHTTPServer", "func:StartServer",
		"func:RunServer", "func:ListenAndServeHTTP",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s in sole-serve inventory, got %#v", id, got)
		}
	}
	for _, id := range []string{
		"func:prepareStandardHandler", "func:ComposeStandardHTTP", "func:MountFrontends", "func:ServeHTTP",
	} {
		if findingsContainIdentity(got, id) {
			t.Fatalf("composition/handler helper must not count as serve API (%s), got %#v", id, got)
		}
	}

	other, err := scanTask43SoleServeAPISource("internal/core/runtime/app.go", `package runtime
func RunWithRuntime() {}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("non-stdhttp path must be ignored, got %#v", other)
	}
}

func TestTask43Detector_SoleServeAPI_PackageScopeServeLikeVars(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
func serveCompat() error { return nil }
var RunWithRuntime = serveCompat
var ServeDataPlane = serveCompat
var (
	otherHelper = 1
	StartHTTPServer func() error
)
const Serve = "compat"
type RunServer struct{}
var listenAndServe = func() error { return nil }
var UnrelatedHelper = serveCompat
func ComposeStandardHTTP() {}
`
	got, err := scanTask43SoleServeAPISource("internal/stdhttp/sneak_serve_var.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"var:RunWithRuntime", "var:ServeDataPlane", "var:StartHTTPServer",
		"const:Serve", "type:RunServer",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected package-scope serve-like %s, got %#v", id, got)
		}
	}
	for _, id := range []string{
		"var:listenAndServe", "var:UnrelatedHelper", "var:otherHelper", "func:ComposeStandardHTTP", "func:serveCompat",
	} {
		if findingsContainIdentity(got, id) {
			t.Fatalf("negative control must not count as serve API (%s), got %#v", id, got)
		}
	}
}

func TestTask43Detector_DeletedServeSymbols(t *testing.T) {
	t.Parallel()
	decl := `package stdhttp
func RunWithRuntime() error { return nil }
func releaseBuiltResources() {}
func runClosers() {}
func NewStandardHandler() {}
func standardHTTPInputFromBuilt() {}
`
	got, err := scanTask43DeletedServeSource("internal/stdhttp/sneak_deleted.go", decl)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"func:RunWithRuntime", "func:releaseBuiltResources", "func:runClosers",
		"func:NewStandardHandler", "func:standardHTTPInputFromBuilt",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected declaration %s, got %#v", id, got)
		}
	}

	vars := `package stdhttp
func serveCompat() error { return nil }
var RunWithRuntime = serveCompat
var (
	NewStandardHandler = serveCompat
	releaseBuiltResources func()
)
const runClosers = "gone"
type standardHTTPInputFromBuilt struct{}
var UnrelatedHelper = serveCompat
`
	got, err = scanTask43DeletedServeSource("internal/stdhttp/sneak_deleted_var.go", vars)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"var:RunWithRuntime", "var:NewStandardHandler", "var:releaseBuiltResources",
		"const:runClosers", "type:standardHTTPInputFromBuilt",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected package-scope deleted symbol %s, got %#v", id, got)
		}
	}
	if findingsContainIdentity(got, "var:UnrelatedHelper") || findingsContainIdentity(got, "func:serveCompat") {
		t.Fatalf("unrelated symbols must not be flagged as deleted serve decls, got %#v", got)
	}

	call := `package cmd
import "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
func boot() { _ = stdhttp.RunWithRuntime(nil, nil, nil, nil, nil) }
`
	got, err = scanTask43DeletedServeSource("cmd/lipstd/sneak_call.go", call)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "call:boot->stdhttp.RunWithRuntime#1") {
		t.Fatalf("expected RunWithRuntime call finding, got %#v", got)
	}

	ok := `package stdhttp
func RunWithGenerationHost() error { return nil }
var listenAndServe = func() error { return nil }
`
	got, err = scanTask43DeletedServeSource("internal/stdhttp/generation_host.go", ok)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("canonical serve path must not be flagged, got %#v", got)
	}
}

func TestTask43Detector_AppOwnedServeMethods(t *testing.T) {
	t.Parallel()
	src := `package runtime
type App struct{}
func (a *App) Start() error { return nil }
func (a *App) Shutdown() {}
func (a *App) Serve() error { return nil }
func (a *App) ListenAndServe() error { return nil }
func (a App) RunHTTP() error { return nil }
func (a *App) ServeRuntime() error { return nil }
func (a *App) StartHTTPServer() error { return nil }
func (a *App) HostAndServe() error { return nil }
func (a *App) RunDataPlane() error { return nil }
func (x *Other) Serve() {}
func (x *Other) StartHTTPServer() {}
`
	got, err := scanTask43AppOwnedServeSource("internal/core/runtime/app.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"method:App.Serve", "method:App.ListenAndServe", "method:App.RunHTTP",
		"method:App.ServeRuntime", "method:App.StartHTTPServer",
		"method:App.HostAndServe", "method:App.RunDataPlane",
	} {
		if !findingsContainIdentity(got, id) {
			t.Fatalf("expected %s, got %#v", id, got)
		}
	}
	if findingsContainIdentity(got, "method:App.Start") || findingsContainIdentity(got, "method:App.Shutdown") {
		t.Fatalf("plugin Start/Shutdown must remain allowed, got %#v", got)
	}
	if findingsContainIdentity(got, "method:Other.Serve") || findingsContainIdentity(got, "method:Other.StartHTTPServer") {
		t.Fatalf("non-App Serve must not be flagged, got %#v", got)
	}

	outside, err := scanTask43AppOwnedServeSource("internal/stdhttp/app.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(outside) != 0 {
		t.Fatalf("App-owned serve gate must stay scoped to internal/core/runtime, got %#v", outside)
	}
}

func TestTask43Detector_StaleSupportedTestNames(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
func TestNewStandardHandler_serverIdentity_frontends(t *testing.T) {}
func TestSecureSessionDiagnostics_mount_matchesRunWithRuntimePattern(t *testing.T) {}
func TestComposeStandardHTTP_openAIModelsAndModelRegistryDiagMounted(t *testing.T) {}
func helperNewStandardHandler() {}
`
	got, err := scanTask43StaleSupportedTestNamesSource("internal/stdhttp/server_identity_stack_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:TestNewStandardHandler_serverIdentity_frontends") {
		t.Fatalf("expected NewStandardHandler test name finding, got %#v", got)
	}
	if !findingsContainIdentity(got, "func:TestSecureSessionDiagnostics_mount_matchesRunWithRuntimePattern") {
		t.Fatalf("expected RunWithRuntime test name finding, got %#v", got)
	}
	if findingsContainIdentity(got, "func:TestComposeStandardHTTP_openAIModelsAndModelRegistryDiagMounted") {
		t.Fatalf("canonical Compose test name must not be flagged, got %#v", got)
	}
	if findingsContainIdentity(got, "func:helperNewStandardHandler") {
		t.Fatalf("non-Test helpers must not be flagged, got %#v", got)
	}

	arch, err := scanTask43StaleSupportedTestNamesSource("internal/archtest/task41_caller_detector_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(arch) != 0 {
		t.Fatalf("archtest detector fixtures must be out of scope, got %#v", arch)
	}
}
