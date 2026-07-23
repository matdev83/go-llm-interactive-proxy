package archtest

import (
	"strings"
	"testing"
)

// Synthetic fixtures prove scanner precision for Task 3.1 MountContract / BuiltDependency gates.

func TestMountContract_SyntheticBuiltInMountInputDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type mountMetricsInput struct {
	Built *runtimebundle.Built
}
func mountMetrics(in mountMetricsInput) {}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_mount_metrics.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainKindHelper(got.Findings, "input_field_broad_bag", "mountMetrics") &&
		!findingsContainKindHelper(got.Findings, "built_dependency", "mountMetrics") {
		t.Fatalf("expected Built detection on mountMetrics input, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticRequestPlaneParamDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func ComposeStandardHTTP(plane runtimebundle.RequestPlane) {}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_compose.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainKindHelper(got.Findings, "request_plane_dependency", "ComposeStandardHTTP") &&
		!findingsContainKindHelper(got.Findings, "built_dependency", "ComposeStandardHTTP") {
		t.Fatalf("expected RequestPlane detection on ComposeStandardHTTP, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticAliasBuiltDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import rb "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type Broad = *rb.Built
type stackHTTPInput struct{ Bag Broad }
func stackHTTPHandler(in stackHTTPInput) {}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_alias.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainKindHelper(got.Findings, "input_field_broad_bag", "stackHTTPHandler") &&
		!findingsContainKindHelper(got.Findings, "built_dependency", "stackHTTPHandler") {
		t.Fatalf("expected alias Built detection, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticDotImportBuiltDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import . "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type mountDiagnosticsInput struct{ Runtime *Built }
func mountDiagnostics(in mountDiagnosticsInput) {}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_dot.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainKindHelper(got.Findings, "input_field_broad_bag", "mountDiagnostics") &&
		!findingsContainKindHelper(got.Findings, "built_dependency", "mountDiagnostics") {
		t.Fatalf("expected dot-import Built detection, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticRenamedLocalBroadBagDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type RuntimeBag struct{ Built *runtimebundle.Built }
type mountAccountingAdminInput struct{ Deps RuntimeBag }
func mountAccountingAdmin(in mountAccountingAdminInput) {}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_renamed.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainKindHelper(got.Findings, "input_field_broad_bag", "mountAccountingAdmin") &&
		!findingsContainKindHelper(got.Findings, "built_dependency", "mountAccountingAdmin") {
		t.Fatalf("expected renamed local broad-bag detection, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticFocusedGroupsClean(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type Executor struct{}
type AuthProvider struct{}
type MetricsBundle struct{}
type Catalog struct{}
type Registry struct{}
type HTTPCoreInput struct{ Executor *Executor }
type HTTPSecurityInput struct{ AuthProviders []AuthProvider }
type HTTPOperationsInput struct{ Metrics *MetricsBundle }
type HTTPModelInput struct{ Catalog *Catalog }
type HTTPFrontendInput struct{ Registry *Registry }
type StandardHTTPInput struct {
	Core HTTPCoreInput
	Security HTTPSecurityInput
	Operations HTTPOperationsInput
	Models HTTPModelInput
	Frontends HTTPFrontendInput
}
func mountMetrics(ops HTTPOperationsInput) {}
func stackHTTPHandler(sec HTTPSecurityInput) {}
func MountBundledFrontends(fe HTTPFrontendInput) {}
func ComposeStandardHTTP(in StandardHTTPInput) {}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_clean.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range desiredStandardHTTPTypes {
		if !got.DeclaredTypes[name] {
			t.Fatalf("clean fixture missing declared type %s", name)
		}
	}
	for _, f := range got.Findings {
		if f.Kind == "built_dependency" || f.Kind == "request_plane_dependency" || f.Kind == "input_field_broad_bag" {
			t.Fatalf("clean focused groups must not report broad-bag findings: %s", f)
		}
		if f.Kind == "lifecycle_field" || f.Kind == "excess_group" || f.Kind == "pointer_group_field" || f.Kind == "service_locator" ||
			f.Kind == "arbitrary_any_field" || f.Kind == "arbitrary_map_field" || f.Kind == "generic_getter_field" {
			t.Fatalf("clean groups must not report contract findings: %s", f)
		}
	}
	// Ratchet-gate predicate: zero broad findings must pass (GREEN path after Task 3.2).
	bad := collectFindingsByKinds(got.Findings, broadBagFindingKinds)
	if len(bad) > 0 {
		t.Fatalf("clean fixture must pass BuiltDependency gate predicate, got %v", bad)
	}
}

func TestMountContract_SyntheticLifecycleFieldDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "io"
type HTTPOperationsInput struct {
	Metrics any
	Closers []func() error
	Closer io.Closer
}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_life.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !got.DeclaredTypes["HTTPOperationsInput"] {
		t.Fatal("expected HTTPOperationsInput declared")
	}
	found := false
	for _, f := range got.Findings {
		if f.Kind == "lifecycle_field" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected lifecycle_field findings, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticLifecycleNeutralCallbackNamesDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type HTTPOperationsInput struct {
	Cleanup func() error
	Release func()
}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_life_neutral.go", src)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, f := range got.Findings {
		if f.Kind == "lifecycle_field" {
			kinds = append(kinds, f.Detail)
		}
	}
	if len(kinds) < 2 {
		t.Fatalf("expected lifecycle_field for Cleanup and Release, got %#v", got.Findings)
	}
	joined := strings.Join(kinds, "\n")
	if !strings.Contains(joined, "Cleanup") || !strings.Contains(joined, "Release") {
		t.Fatalf("expected Cleanup and Release in findings, got %v", kinds)
	}
}

func TestMountContract_SyntheticUnrelatedBuiltIgnored(t *testing.T) {
	t.Parallel()
	src := `package otherpkg
type Built struct{}
func helper(b *Built) {}
func mountMetrics(b *Built) {} // not stdhttp contract path; still parsed but Built is local non-runtimebundle
`
	got, err := scanMountContractSource("internal/other/synthetic_unrelated.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got.Findings {
		if strings.Contains(f.Detail, "runtimebundle") || f.Kind == "built_dependency" {
			// Local Built without runtimebundle import must not false-positive as runtimebundle.Built
			if strings.Contains(f.Detail, "runtimebundle.Built") {
				t.Fatalf("unrelated local Built false positive: %s", f)
			}
		}
	}
}

func TestMountContract_SyntheticExcessGroupDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type HTTPOperationsInput struct{}
type HTTPSecurityInput struct{}
type mountMetricsInput struct {
	Ops HTTPOperationsInput
	Sec HTTPSecurityInput
}
func mountMetrics(in mountMetricsInput) {}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_excess.go", src)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range got.Findings {
		if f.Kind == "excess_group" && f.Helper == "mountMetrics" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected excess_group for mountMetrics accepting Security, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticDirectParamExcessGroup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		src     string
		wantBad bool
	}{
		{
			name: "mountMetrics_Operations_clean",
			src: `package stdhttp
type HTTPOperationsInput struct{}
func mountMetrics(ops HTTPOperationsInput) {}
`,
			wantBad: false,
		},
		{
			name: "mountMetrics_Security_excess",
			src: `package stdhttp
type HTTPSecurityInput struct{}
func mountMetrics(sec HTTPSecurityInput) {}
`,
			wantBad: true,
		},
		{
			name: "mountMetrics_StandardHTTPInput_excess",
			src: `package stdhttp
type StandardHTTPInput struct{}
func mountMetrics(in StandardHTTPInput) {}
`,
			wantBad: true,
		},
		{
			name: "ComposeStandardHTTP_StandardHTTPInput_clean",
			src: `package stdhttp
type StandardHTTPInput struct{}
func ComposeStandardHTTP(in StandardHTTPInput) {}
`,
			wantBad: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := scanMountContractSource("internal/stdhttp/synthetic_direct_"+tc.name+".go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range got.Findings {
				if f.Kind == "excess_group" {
					found = true
					break
				}
			}
			if found != tc.wantBad {
				t.Fatalf("excess_group found=%v wantBad=%v findings=%#v", found, tc.wantBad, got.Findings)
			}
		})
	}
}

func TestMountContract_SyntheticArbitraryAnyFieldDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type HTTPOperationsInput struct {
	Metrics *int
	TokenAccountingAdmin any
	Bag interface{}
}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_any.go", src)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, f := range got.Findings {
		if f.Kind == "arbitrary_any_field" {
			details = append(details, f.Detail)
		}
	}
	if len(details) < 2 {
		t.Fatalf("expected arbitrary_any_field for any and interface{}, got %#v", got.Findings)
	}
	joined := strings.Join(details, "\n")
	if !strings.Contains(joined, "TokenAccountingAdmin") || !strings.Contains(joined, "Bag") {
		t.Fatalf("expected TokenAccountingAdmin and Bag in findings, got %v", details)
	}
}

func TestMountContract_SyntheticArbitraryMapAndGetterDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type HTTPCoreInput struct {
	ByName map[string]string
	GetService func(string) int
}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_map_getter.go", src)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, f := range got.Findings {
		if f.Kind == "arbitrary_map_field" || f.Kind == "generic_getter_field" {
			kinds = append(kinds, f.Kind+":"+f.Detail)
		}
	}
	if len(kinds) < 2 {
		t.Fatalf("expected map and getter findings, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticServiceLocatorDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type Resolver interface{ Get(string) any }
type HTTPCoreInput struct{ Services Resolver }
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_locator.go", src)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range got.Findings {
		if f.Kind == "service_locator" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected service_locator for Resolver.Get, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticCohesiveCapabilityInterfaceAllowed(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type Request struct{}
type Executor interface {
	Execute(ctx context.Context, req Request) error
}
type HTTPCoreInput struct{ Exec Executor }
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_capability.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got.Findings {
		if f.Kind == "service_locator" {
			t.Fatalf("cohesive capability interface must not be service_locator: %s", f)
		}
	}
}

func TestMountContract_SyntheticStandardHTTPInputPointerGroupRejected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type HTTPCoreInput struct{}
type HTTPSecurityInput struct{}
type HTTPOperationsInput struct{}
type HTTPModelInput struct{}
type HTTPFrontendInput struct{}
type StandardHTTPInput struct {
	Core *HTTPCoreInput
	Security HTTPSecurityInput
	Operations HTTPOperationsInput
	Models HTTPModelInput
	Frontends HTTPFrontendInput
}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_ptr_group.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if got.StandardHTTPFields["Core"] != "HTTPCoreInput" {
		t.Fatalf("Core type name=%q want HTTPCoreInput", got.StandardHTTPFields["Core"])
	}
	if !got.StandardHTTPFieldIsPointer["Core"] {
		t.Fatal("Core must be recorded as pointer shape")
	}
	found := false
	for _, f := range got.Findings {
		if f.Kind == "pointer_group_field" && strings.Contains(f.Detail, "Core") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected pointer_group_field for Core, got %#v", got.Findings)
	}
}

func TestMountContract_SyntheticStandardHTTPInputValueGroupsClean(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type HTTPCoreInput struct{}
type HTTPSecurityInput struct{}
type HTTPOperationsInput struct{}
type HTTPModelInput struct{}
type HTTPFrontendInput struct{}
type StandardHTTPInput struct {
	Core HTTPCoreInput
	Security HTTPSecurityInput
	Operations HTTPOperationsInput
	Models HTTPModelInput
	Frontends HTTPFrontendInput
}
`
	got, err := scanMountContractSource("internal/stdhttp/synthetic_value_group.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range desiredStandardHTTPGroupFields {
		if got.StandardHTTPFields[field] != want {
			t.Fatalf("%s type=%q want %s", field, got.StandardHTTPFields[field], want)
		}
		if got.StandardHTTPFieldIsPointer[field] {
			t.Fatalf("%s must be value, not pointer", field)
		}
	}
	for _, f := range got.Findings {
		if f.Kind == "pointer_group_field" {
			t.Fatalf("value groups must not report pointer_group_field: %s", f)
		}
	}
}

func findingsContainKindHelper(fs []mountContractFinding, kind, helper string) bool {
	for _, f := range fs {
		if f.Kind == kind && f.Helper == helper {
			return true
		}
	}
	return false
}

// Policy classification proofs: scanner still detects transitional adapter bags,
// but Task 3.2 strict failure sets only include mount/composer surfaces.

func TestMountContract_Policy_StrictMountBroadBagFails(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type mountMetricsInput struct{ Built *runtimebundle.Built }
func mountMetrics(in mountMetricsInput) {}
`
	got, err := scanMountContractSource("internal/stdhttp/policy_mount_metrics.go", src)
	if err != nil {
		t.Fatal(err)
	}
	strict := collectStrictTask32Findings(got.Findings, task32StrictFailureKinds)
	if len(strict) == 0 {
		t.Fatalf("broad mountMetrics must fail strict Task 3.2 policy, got findings %#v", got.Findings)
	}
	joined := strings.Join(strict, "\n")
	if !strings.Contains(joined, "mountMetrics") {
		t.Fatalf("strict failures must name mountMetrics, got %v", strict)
	}
}

func TestMountContract_Policy_StrictPrepareBroadBagFails(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func prepareStandardHandler(built *runtimebundle.Built) {}
`
	got, err := scanMountContractSource("internal/stdhttp/policy_prepare.go", src)
	if err != nil {
		t.Fatal(err)
	}
	strict := collectStrictTask32Findings(got.Findings, task32StrictFailureKinds)
	if len(strict) == 0 {
		t.Fatalf("broad prepareStandardHandler must fail strict Task 3.2 policy, got %#v", got.Findings)
	}
	if !strings.Contains(strings.Join(strict, "\n"), "prepareStandardHandler") {
		t.Fatalf("strict failures must name prepareStandardHandler, got %v", strict)
	}
}

func TestMountContract_Policy_TransitionalBuiltAdapterTrackedNotStrict(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func NewStandardHandler(built *runtimebundle.Built) {}
func ComposeStandardHTTP(plane runtimebundle.RequestPlane) {}
`
	got, err := scanMountContractSource("internal/stdhttp/policy_adapters.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainKindHelper(got.Findings, "built_dependency", "NewStandardHandler") {
		t.Fatalf("scanner must still detect NewStandardHandler Built, got %#v", got.Findings)
	}
	if !findingsContainKindHelper(got.Findings, "request_plane_dependency", "ComposeStandardHTTP") {
		t.Fatalf("scanner must detect RequestPlane on ComposeStandardHTTP, got %#v", got.Findings)
	}
	tracked := collectTransitionalAdapterFindings(got.Findings, broadBagFindingKinds)
	if len(tracked) != 1 || !strings.Contains(strings.Join(tracked, "\n"), "NewStandardHandler") {
		t.Fatalf("expected only NewStandardHandler tracked as transitional, got %v (all=%#v)", tracked, got.Findings)
	}
	strict := collectStrictTask32Findings(got.Findings, task32StrictFailureKinds)
	if len(strict) == 0 || !strings.Contains(strings.Join(strict, "\n"), "ComposeStandardHTTP") {
		t.Fatalf("ComposeStandardHTTP RequestPlane must be a Task 3.2/3.5 strict failure, got %v", strict)
	}
	if strings.Contains(strings.Join(strict, "\n"), "NewStandardHandler") {
		t.Fatalf("NewStandardHandler Built must remain non-strict until Phase 4, got %v", strict)
	}
}

func TestMountContract_Policy_MountBroadBagNotHiddenByAdapters(t *testing.T) {
	t.Parallel()
	// Adapters projecting to mounts must not hide Built still present on mount
	// input structs — strict policy still fails the mount surface.
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type mountMetricsInput struct{ Built *runtimebundle.Built }
func mountMetrics(in mountMetricsInput) {}
func NewStandardHandler(built *runtimebundle.Built) {}
func prepareStandardHandler(in StandardHTTPInput) {}
type StandardHTTPInput struct{}
`
	got, err := scanMountContractSource("internal/stdhttp/policy_hide.go", src)
	if err != nil {
		t.Fatal(err)
	}
	strict := collectStrictTask32Findings(got.Findings, task32StrictFailureKinds)
	if len(strict) == 0 {
		t.Fatal("mountMetrics Built must remain a strict failure even when adapters are present")
	}
	joined := strings.Join(strict, "\n")
	if !strings.Contains(joined, "mountMetrics") {
		t.Fatalf("strict set must include mountMetrics, got %v", strict)
	}
	if strings.Contains(joined, "NewStandardHandler") {
		t.Fatalf("strict set must exclude Phase 4 Built adapter, got %v", strict)
	}
	if strings.Contains(joined, "prepareStandardHandler") {
		t.Fatalf("focused prepareStandardHandler(StandardHTTPInput) must not be strict-failed, got %v", strict)
	}
}
