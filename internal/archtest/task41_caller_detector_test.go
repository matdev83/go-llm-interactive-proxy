package archtest

import (
	"strings"
	"testing"
)

func TestTask41Detector_BuildCallDetectsSelectorAndDotImport(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import rb "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func CallCompat() { _, _ = rb.Build(nil, nil, nil, nil) }
`
	got, err := scanTask41BuildCallSource("internal/infra/runtimebundle/caller.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentityPrefix(got, "call:Build@") {
		t.Fatalf("expected Build call detection, got %#v", got)
	}

	same := `package runtimebundle
func CallSame() { _, _ = Build(nil, nil, nil, nil) }
`
	got, err = scanTask41BuildCallSource("internal/infra/runtimebundle/bootstrap_plan.go", same)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentityPrefix(got, "call:Build@") {
		t.Fatalf("expected same-package Build call detection, got %#v", got)
	}

	dot := `package runtimebundle
import . "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func CallDot() { _, _ = Build(nil, nil, nil, nil) }
`
	got, err = scanTask41BuildCallSource("internal/core/runtime/dot_caller.go", dot)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentityPrefix(got, "call:Build@") {
		t.Fatalf("expected dot-import Build call detection, got %#v", got)
	}

	decl, err := scanTask41BuildCallSource("internal/infra/runtimebundle/build.go", `package runtimebundle
func Build() {}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(decl) != 0 {
		t.Fatalf("declaration file must be excluded, got %#v", decl)
	}

	// Unrelated packages' Build must not be flagged.
	other := `package modelregistry
func Build() {}
func Use() { Build() }
`
	got, err = scanTask41BuildCallSource("internal/core/modelregistry/build.go", other)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated Build must not be flagged, got %#v", got)
	}
}

func TestTask41Detector_BuiltCarrierIgnoresScheduledProducers(t *testing.T) {
	t.Parallel()
	foreign := `package lipstd
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type Hold struct { B *runtimebundle.Built }
func Take(b *runtimebundle.Built) {}
`
	got, err := scanTask41BuiltCarrierSource("cmd/lipstd/hold.go", foreign)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "type:Hold") || !findingsContainIdentity(got, "func:Take") {
		t.Fatalf("expected Built carrier findings, got %#v", got)
	}

	scheduled, err := scanTask41BuiltCarrierSource("internal/stdhttp/handler.go", `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func NewStandardHandler(b *runtimebundle.Built) {}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 0 {
		t.Fatalf("scheduled stdhttp producer must be excluded, got %#v", scheduled)
	}
}

func TestTask41Detector_TestLegacyCallersQualified(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle_test
import (
  "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
  "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
)
func TestLegacy(t *testing.T) {
  _, _ = runtimebundle.Build(nil, nil, nil, nil)
  _ = &runtimebundle.Built{}
  _, _, _ = stdhttp.NewStandardHandler(nil, nil, nil, nil, nil)
  _ = stdhttp.RunWithRuntime(nil, nil, nil, nil, nil)
}
`
	got, err := scanTask41TestLegacyCallerSource("internal/infra/runtimebundle/legacy_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"call:Build@", "lit:Built@", "call:NewStandardHandler@", "call:RunWithRuntime@"} {
		if !findingsContainIdentityPrefix(got, needle) {
			t.Fatalf("expected %s in %#v", needle, got)
		}
	}
}

func TestTask41Detector_SamePackageUnqualifiedLegacyCallers(t *testing.T) {
	t.Parallel()
	rbSrc := `package runtimebundle
func TestLeak(t *testing.T) {
  _, _ = Build(nil, nil, nil, nil)
  _ = &Built{}
  var b *Built
  _ = b
}
type HoldBuilt struct { X *Built }
`
	got, err := scanTask41TestLegacyCallerSource("internal/infra/runtimebundle/control_plane_closer_leak_test.go", rbSrc)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentityPrefix(got, "call:Build@") {
		t.Fatalf("expected unqualified same-package Build, got %#v", got)
	}
	if !findingsContainIdentityPrefix(got, "lit:Built@") && !findingsContainIdentity(got, "type:HoldBuilt") {
		t.Fatalf("expected unqualified Built carrier/literal, got %#v", got)
	}

	httpSrc := `package stdhttp
func TestCompat(t *testing.T) {
  _, _, _ = NewStandardHandler(nil, nil, nil, nil, nil)
  _ = RunWithRuntime(nil, nil, nil, nil, nil)
}
`
	got, err = scanTask41TestLegacyCallerSource("internal/stdhttp/compat_test.go", httpSrc)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentityPrefix(got, "call:NewStandardHandler@") {
		t.Fatalf("expected unqualified NewStandardHandler, got %#v", got)
	}
	if !findingsContainIdentityPrefix(got, "call:RunWithRuntime@") {
		t.Fatalf("expected unqualified RunWithRuntime, got %#v", got)
	}
}

func TestTask41Detector_LiveCallInDetectorNamedFileMustFail(t *testing.T) {
	t.Parallel()
	// No filename bypass: AST source strings are ignored naturally, but a live
	// call inside a detector-named file must still be flagged.
	src := `package archtest
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func TestLive(t *testing.T) {
  _, _ = runtimebundle.Build(nil, nil, nil, nil)
}
`
	got, err := scanTask41TestLegacyCallerSource("internal/archtest/task41_caller_detector_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentityPrefix(got, "call:Build@") {
		t.Fatalf("live call in detector-named file must fail, got %#v", got)
	}
}

func TestTask41Detector_ReplacementAggregateNameIndependent(t *testing.T) {
	t.Parallel()
	named := `package runtimebundle_test
type TestBuiltBag struct {
  Executor any
  Store any
  Closers any
  PluginRegistry any
  RuntimeSnapshot any
  Metrics any
  Deps map[string]any
}
`
	got, err := scanTask41ReplacementAggregateSource("internal/infra/runtimebundle/bag_test.go", named)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "type:TestBuiltBag") {
		t.Fatalf("expected named replacement aggregate detection, got %#v", got)
	}

	// Exact innocuous name that previously evaded the gate.
	innocuous := `package stdhttp
type testHTTPRuntimeFields struct {
  Executor any
  Store any
  PluginRegistry any
  Metrics any
  CatalogRuntime any
  TokenAccountingAdmin any
  UsageAuthority any
}
`
	got, err = scanTask41ReplacementAggregateSource("internal/stdhttp/handler_compose_test_helpers_test.go", innocuous)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "type:testHTTPRuntimeFields") {
		t.Fatalf("name-independent Built-surface bag must fail, got %#v", got)
	}

	// Legitimate small fixture with one any field must not fail.
	small := `package stdhttp
type routeCase struct {
  Name string
  Want int
  Extra any
}
`
	got, err = scanTask41ReplacementAggregateSource("internal/stdhttp/route_case_test.go", small)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("small fixture must not be flagged, got %#v", got)
	}
}

func TestTask41Detector_LifecycleComposeHelperCombined(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "context"
func composeTestStandardHandler(ctx context.Context, app *App) {
  _, _ = ComposeStandardHTTP(ctx, nil, nil, StandardHTTPInput{})
  _ = app.Start(ctx)
  app.Shutdown(ctx)
}
func TestOk(t *testing.T) {
  // Separate steps in a Test* function are allowed.
  _ = app.Start(ctx)
  _, _ = ComposeStandardHTTP(ctx, nil, nil, StandardHTTPInput{})
}
func startTestApp(app *App) { _ = app.Start(nil) }
func composeOnly(ctx context.Context) { _, _ = ComposeStandardHTTP(ctx, nil, nil, StandardHTTPInput{}) }
`
	got, err := scanTask41LifecycleComposeHelperSource("internal/stdhttp/handler_compose_test_helpers_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:composeTestStandardHandler") {
		t.Fatalf("expected combined helper detection, got %#v", got)
	}
	if findingsContainIdentity(got, "func:startTestApp") || findingsContainIdentity(got, "func:composeOnly") {
		t.Fatalf("split helpers must not be flagged, got %#v", got)
	}
	if findingsContainIdentity(got, "func:TestOk") {
		t.Fatalf("Test* entrypoints must not be flagged, got %#v", got)
	}
}

func findingsContainIdentityPrefix(got []convergenceFinding, prefix string) bool {
	for _, f := range got {
		if f.Identity == prefix || strings.HasPrefix(f.Identity, prefix) {
			return true
		}
	}
	return false
}
