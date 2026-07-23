package archtest

import (
	"strings"
	"testing"
)

func TestTask35_SyntheticBroadRequestPlaneDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type RequestPlane struct{ executor any }
func (p RequestPlane) Executor() any { return p.executor }
func NewCompatRequestPlane() RequestPlane { return RequestPlane{} }
`
	got, err := scanBroadRequestPlaneSource("internal/infra/runtimebundle/synthetic_plane.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "type:RequestPlane") {
		t.Fatalf("expected type:RequestPlane, got %v", got)
	}
	if !findingsContainIdentity(got, "method:RequestPlane.Executor") {
		t.Fatalf("expected getter-wall method, got %v", got)
	}
	if !findingsContainIdentity(got, "func:NewCompatRequestPlane") {
		t.Fatalf("expected NewCompatRequestPlane, got %v", got)
	}
}

func TestTask35_SyntheticCompatSymbolsDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
func ComposeRequestPlane() {}
func standardHTTPInputFromRequestPlane() {}
func use() { ComposeRequestPlane(); standardHTTPInputFromRequestPlane() }
`
	got, err := scanCompatHTTPSymbolsSource("internal/stdhttp/synthetic_compat.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:ComposeRequestPlane") {
		t.Fatalf("expected ComposeRequestPlane decl, got %v", got)
	}
	if !findingsContainIdentity(got, "func:standardHTTPInputFromRequestPlane") {
		t.Fatalf("expected projector decl, got %v", got)
	}
	if !findingsContainIdentity(got, "call:use->ComposeRequestPlane#1") {
		t.Fatalf("expected ComposeRequestPlane call, got %v", got)
	}
}

func TestTask35_SyntheticFocusedLifecycleBagDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
type StandardHTTPInput struct {
	Closers []func() error
	Deps map[string]any
}
func ComposeStandardHTTP(closers []func() error, bag map[string]any) {
	_ = closers
	_ = bag
}
`
	got, err := scanFocusedHTTPLifecycleSource("internal/stdhttp/synthetic_life.go", src)
	if err != nil {
		t.Fatal(err)
	}
	joined := findingsJoin(got)
	if !strings.Contains(joined, "Closers") && !strings.Contains(joined, "generic") && !strings.Contains(joined, "Deps") {
		t.Fatalf("expected lifecycle/generic-bag findings, got %v", got)
	}
}

func TestTask35_SyntheticNewBuiltFunctionRejected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func NewExtraBuiltAdapter(built *runtimebundle.Built) {}
`
	got, err := scanStdhttpBuiltSource("internal/stdhttp/synthetic_built.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:NewExtraBuiltAdapter") {
		t.Fatalf("expected new Built dependency detection, got %v", got)
	}
	allow := []convergenceAllowlistEntry{{
		Gate: gateStdhttpBuilt, Path: "internal/stdhttp/handler.go",
		Identity: "func:NewStandardHandler", Classification: classDeclaration,
		RetirementTask: "4.1", Rationale: "unrelated",
	}}
	bad := convergenceAllowlistDrift(gateStdhttpBuilt, got, allow)
	if len(bad) == 0 {
		t.Fatal("expected allowlist drift for new Built function outside Phase 4 grandfather list")
	}
}

// Gap A: parameter-only scanners miss body construction, conversions, type/var
// declarations, import aliases, and dot-imports.
func TestTask35_SyntheticBuiltBodyConstructionDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func NewLegacyThing() any { return &runtimebundle.Built{} }
func ConvertLegacy() any { return (*runtimebundle.Built)(nil) }
`
	got, err := scanStdhttpBuiltSource("internal/stdhttp/synthetic_built_body.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:NewLegacyThing") {
		t.Fatalf("expected body construction finding, got %v", got)
	}
	if !findingsContainIdentity(got, "func:ConvertLegacy") {
		t.Fatalf("expected conversion finding, got %v", got)
	}
}

func TestTask35_SyntheticBuiltTypeAndVarDetected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
type LegacyHolder struct { Built *runtimebundle.Built }
type BuiltAlias = *runtimebundle.Built
var Legacy = (*runtimebundle.Built)(nil)
`
	got, err := scanStdhttpBuiltSource("internal/stdhttp/synthetic_built_type.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "type:LegacyHolder") {
		t.Fatalf("expected struct-field finding, got %v", got)
	}
	if !findingsContainIdentity(got, "type:BuiltAlias") {
		t.Fatalf("expected type-alias finding, got %v", got)
	}
	if !findingsContainIdentity(got, "var:Legacy") {
		t.Fatalf("expected var finding, got %v", got)
	}
}

func TestTask35_SyntheticBuiltImportAliasAndDotImportDetected(t *testing.T) {
	t.Parallel()
	aliased := `package stdhttp
import rb "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func UseAliased(b rb.Built) {}
func ConstructAliased() any { return &rb.Built{} }
`
	got, err := scanStdhttpBuiltSource("internal/stdhttp/synthetic_built_alias.go", aliased)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:UseAliased") {
		t.Fatalf("expected aliased non-pointer param finding, got %v", got)
	}
	if !findingsContainIdentity(got, "func:ConstructAliased") {
		t.Fatalf("expected aliased body construction finding, got %v", got)
	}

	dotted := `package stdhttp
import . "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func UseDot(b *Built) {}
func ConstructDot() any { return &Built{} }
`
	got, err = scanStdhttpBuiltSource("internal/stdhttp/synthetic_built_dot.go", dotted)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:UseDot") {
		t.Fatalf("expected dot-import param finding, got %v", got)
	}
	if !findingsContainIdentity(got, "func:ConstructDot") {
		t.Fatalf("expected dot-import body construction finding, got %v", got)
	}
}

func TestTask35_SyntheticBuiltSameFileNewDeclarationRejected(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
func NewStandardHandler(built *runtimebundle.Built) {}
func NewExtraSameFile() any { return &runtimebundle.Built{} }
`
	got, err := scanStdhttpBuiltSource("internal/stdhttp/handler.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:NewExtraSameFile") {
		t.Fatalf("expected same-file new declaration finding, got %v", got)
	}
	allow := []convergenceAllowlistEntry{{
		Gate: gateStdhttpBuilt, Path: "internal/stdhttp/handler.go",
		Identity: "func:NewStandardHandler", Classification: classDeclaration,
		RetirementTask: "4.1", Rationale: "Phase 4 grandfather only",
	}}
	bad := convergenceAllowlistDrift(gateStdhttpBuilt, got, allow)
	if len(bad) == 0 {
		t.Fatal("expected allowlist drift for new same-file Built declaration")
	}
}

func TestTask35_SyntheticCanonicalCloserRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func CompileGeneration() {
	_ = cand.Closers
	_ = ledger.LegacyClosers()
}
`
	got, err := scanCanonicalGenerationClosersSource("internal/infra/runtimebundle/compile_generation.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "sel:CompileGeneration->Closers") {
		t.Fatalf("expected Closers finding, got %v", got)
	}
	if !findingsContainIdentity(got, "sel:CompileGeneration->LegacyClosers") {
		t.Fatalf("expected LegacyClosers finding, got %v", got)
	}
}

// Gap B: canonical-only closer scanners miss new production helpers elsewhere.
func TestTask35_SyntheticCandidateLegacyClosersOutsideCanonicalRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func HelperOutsideCanonical(cand *CandidateRuntime, ledger *ResourceLedger) {
	_ = cand.Closers
	_ = ledger.LegacyClosers()
}
`
	// Canonical scanner must not be treated as sufficient coverage.
	canon, err := scanCanonicalGenerationClosersSource("internal/infra/runtimebundle/helper_outside.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(canon) != 0 {
		t.Fatalf("canonical scanner should ignore non-canonical files, got %v", canon)
	}
	got, err := scanCandidateLegacyClosersSource("internal/infra/runtimebundle/helper_outside.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:HelperOutsideCanonical") {
		t.Fatalf("expected repo-wide closer finding, got %v", got)
	}
	allow := []convergenceAllowlistEntry{{
		Gate: gateCandidateLegacyClosers, Path: "internal/infra/runtimebundle/build.go",
		Identity: "func:Build", Classification: classDeclaration,
		RetirementTask: "4.2", Rationale: "unrelated Phase 4 site",
	}}
	bad := convergenceAllowlistDrift(gateCandidateLegacyClosers, got, allow)
	if len(bad) == 0 {
		t.Fatal("expected allowlist drift for new candidate legacy closer use")
	}
}

func TestTask35_SyntheticCandidateLegacyClosersRenamedReceiverDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func (widget *CandidateRuntime) sneak() {
	_ = widget.Closers
}
func (bag *ResourceLedger) sneakLegacy() {
	_ = bag.LegacyClosers()
}
`
	got, err := scanCandidateLegacyClosersSource("internal/infra/runtimebundle/sneak.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "method:CandidateRuntime.sneak") {
		t.Fatalf("expected renamed Closers receiver finding, got %v", got)
	}
	if !findingsContainIdentity(got, "method:ResourceLedger.sneakLegacy") {
		t.Fatalf("expected renamed LegacyClosers receiver finding, got %v", got)
	}
}

func TestTask35_CandidateLegacyClosers_StaleGrandfatherFails(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func Unrelated() {}
`
	got, err := scanCandidateLegacyClosersSource("internal/infra/runtimebundle/build.go", src)
	if err != nil {
		t.Fatal(err)
	}
	allow := []convergenceAllowlistEntry{{
		Gate: gateCandidateLegacyClosers, Path: "internal/infra/runtimebundle/build.go",
		Identity: "func:Build", Classification: classDeclaration,
		RetirementTask: "4.2", Rationale: "stale after deletion",
	}}
	bad := convergenceAllowlistDrift(gateCandidateLegacyClosers, got, allow)
	if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), "stale allowlist entry") {
		t.Fatalf("expected stale candidate_legacy_closers rejection, got %v", bad)
	}
}

func TestTask35_StdhttpBuilt_StaleGrandfatherFails(t *testing.T) {
	t.Parallel()
	src := `package stdhttp
func Unrelated() {}
`
	got, err := scanStdhttpBuiltSource("internal/stdhttp/handler.go", src)
	if err != nil {
		t.Fatal(err)
	}
	allow := []convergenceAllowlistEntry{{
		Gate: gateStdhttpBuilt, Path: "internal/stdhttp/handler.go",
		Identity: "func:NewStandardHandler", Classification: classDeclaration,
		RetirementTask: "4.1", Rationale: "stale after deletion",
	}}
	bad := convergenceAllowlistDrift(gateStdhttpBuilt, got, allow)
	if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), "stale allowlist entry") {
		t.Fatalf("expected stale Phase 4 grandfather rejection, got %v", bad)
	}
}

func findingsJoin(fs []convergenceFinding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.String())
		b.WriteByte('\n')
	}
	return b.String()
}
