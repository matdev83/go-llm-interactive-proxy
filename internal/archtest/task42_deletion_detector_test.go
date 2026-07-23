package archtest

import "testing"

func TestTask42Detector_BuiltTypeDeclaration(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type Built struct {
	Executor any
}
`
	got, err := scanTask42BuiltTypeDeclSource("internal/infra/runtimebundle/sneak_built.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "type:Built") {
		t.Fatalf("expected type:Built finding, got %#v", got)
	}

	other := `package modelregistry
type Built struct{}
`
	got, err = scanTask42BuiltTypeDeclSource("internal/core/modelregistry/build.go", other)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated package Built must not be flagged, got %#v", got)
	}
}

func TestTask42Detector_BuildDeclaration(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func Build() (*int, error) { return nil, nil }
func (p *ProcessServices) Build() {} // method must not be flagged
`
	got, err := scanTask42BuildDeclSource("internal/infra/runtimebundle/sneak_build.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:Build") {
		t.Fatalf("expected func:Build finding, got %#v", got)
	}
	if len(got) != 1 {
		t.Fatalf("method Build must not be flagged, got %#v", got)
	}

	varAlias := `package runtimebundle
func buildCompatibility() (*int, error) { return nil, nil }
var Build = buildCompatibility
`
	got, err = scanTask42BuildDeclSource("internal/infra/runtimebundle/sneak_var_build.go", varAlias)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "var:Build") {
		t.Fatalf("package-scope var Build must be rejected, got %#v", got)
	}

	groupedVar := `package runtimebundle
var (
	other = 1
	Build func() error
)
`
	got, err = scanTask42BuildDeclSource("internal/infra/runtimebundle/sneak_grouped_var.go", groupedVar)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "var:Build") {
		t.Fatalf("grouped var Build must be rejected, got %#v", got)
	}

	constBuild := `package runtimebundle
const Build = "compat"
`
	got, err = scanTask42BuildDeclSource("internal/infra/runtimebundle/sneak_const_build.go", constBuild)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "const:Build") {
		t.Fatalf("package-scope const Build must be rejected, got %#v", got)
	}

	typeBuild := `package runtimebundle
type Build struct{}
`
	got, err = scanTask42BuildDeclSource("internal/infra/runtimebundle/sneak_type_build.go", typeBuild)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "type:Build") {
		t.Fatalf("package-scope type Build must be rejected, got %#v", got)
	}

	other := `package lipruntime
func Build() {}
var Build = Build
`
	got, err = scanTask42BuildDeclSource("pkg/lipruntime/build.go", other)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("lipruntime.Build must not be flagged (out of runtimebundle scope), got %#v", got)
	}

	modelReg := `package modelregistry
func Build() {}
`
	got, err = scanTask42BuildDeclSource("internal/core/modelregistry/build.go", modelReg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("modelregistry.Build must not be flagged, got %#v", got)
	}
}

func TestTask42Detector_CandidateCloserFieldByNameOrShape(t *testing.T) {
	t.Parallel()
	byName := `package runtimebundle
type CandidateRuntime struct {
	Closers []func() error
}
`
	got, err := scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/candidate_compile.go", byName)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:CandidateRuntime.Closers") {
		t.Fatalf("expected named Closers field finding, got %#v", got)
	}

	unexportedExact := `package runtimebundle
type CandidateRuntime struct {
	teardown []func() error
}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/sneak_candidate.go", unexportedExact)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:CandidateRuntime.teardown") {
		t.Fatalf("unexported []func() error on CandidateRuntime must be rejected, got %#v", got)
	}

	renamedOwner := `package runtimebundle
type GenerationWidget struct {
	Ledger   *ResourceLedger
	teardown []func() error
}
type ResourceLedger struct{}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/sneak_widget.go", renamedOwner)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:GenerationWidget.teardown") {
		t.Fatalf("renamed generation owner with Ledger + closer list must be rejected, got %#v", got)
	}

	broadSurface := `package runtimebundle
type BroadCand struct {
	Executor       any
	Store          any
	PluginRegistry any
	teardown       []func() error
}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/sneak_broad.go", broadSurface)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:BroadCand.teardown") {
		t.Fatalf("renamed broad candidate surface + closer list must be rejected, got %#v", got)
	}

	exportedRename := `package runtimebundle
type GenerationWidget struct {
	Executor any
	Store    any
	Ledger   *ResourceLedger
	TeardownFns []func() error
}
type ResourceLedger struct{}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/sneak_widget2.go", exportedRename)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:GenerationWidget.TeardownFns") {
		t.Fatalf("exported closer list on renamed generation owner must be rejected, got %#v", got)
	}

	processOK := `package runtimebundle
type ProcessServices struct {
	closers []func() error
}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/process_services.go", processOK)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("ProcessServices.closers must remain allowed, got %#v", got)
	}

	tinyHelper := `package runtimebundle
type SmallFixture struct {
	Name string
	fn   []func() error
}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/small.go", tinyHelper)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated tiny helper without generation-owner role must not be flagged, got %#v", got)
	}

	constructionLocal := `package runtimebundle
type startedModelCatalog struct {
	Runtime        any
	closers        []func() error
	quiesceClosers []func() error
}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/modelcatalog_attach.go", constructionLocal)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("construction-local closer slices must remain allowed, got %#v", got)
	}

	aliasField := `package runtimebundle
type CloseFn = func() error
type CandidateRuntime struct {
	teardown []CloseFn
}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/sneak_alias_field.go", aliasField)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:CandidateRuntime.teardown") {
		t.Fatalf("[]CloseFn alias field on CandidateRuntime must be rejected, got %#v", got)
	}

	sliceAlias := `package runtimebundle
type CloseFn = func() error
type CloseBag = []CloseFn
type CandidateRuntime struct {
	teardown CloseBag
}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/sneak_slice_alias.go", sliceAlias)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:CandidateRuntime.teardown") {
		t.Fatalf("CloseBag alias field on CandidateRuntime must be rejected, got %#v", got)
	}

	definedNamed := `package runtimebundle
type DefinedCloseFn func() error
type DefinedCloseBag []DefinedCloseFn
type GenerationWidget struct {
	Ledger   *ResourceLedger
	teardown DefinedCloseBag
}
type ResourceLedger struct{}
`
	got, err = scanTask42CandidateCloserFieldSource("internal/infra/runtimebundle/sneak_defined_alias.go", definedNamed)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "field:GenerationWidget.teardown") {
		t.Fatalf("DefinedCloseBag on generation owner must be rejected, got %#v", got)
	}
}

func TestTask42Detector_LedgerCloserProjection(t *testing.T) {
	t.Parallel()
	named := `package runtimebundle
func (l *ResourceLedger) LegacyClosers() []func() error { return nil }
`
	got, err := scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/resource_ledger.go", named)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "method:ResourceLedger.LegacyClosers") {
		t.Fatalf("expected LegacyClosers method finding, got %#v", got)
	}

	renamed := `package runtimebundle
func (bag *ResourceLedger) ExportClosers() []func() error { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_export.go", renamed)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "method:ResourceLedger.ExportClosers") {
		t.Fatalf("expected renamed ResourceLedger []func() error method finding, got %#v", got)
	}

	valueRecv := `package runtimebundle
func (l ResourceLedger) DumpClosers() []func() error { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_value.go", valueRecv)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "method:ResourceLedger.DumpClosers") {
		t.Fatalf("value-receiver ResourceLedger projection must be rejected, got %#v", got)
	}

	topLevel := `package runtimebundle
func exportLedgerClosers(l *ResourceLedger) []func() error { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_adapter.go", topLevel)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:exportLedgerClosers") {
		t.Fatalf("top-level ResourceLedger→[]func() error adapter must be rejected, got %#v", got)
	}

	topLevelValue := `package runtimebundle
func projectClosers(l ResourceLedger) []func() error { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_adapter2.go", topLevelValue)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:projectClosers") {
		t.Fatalf("top-level value ResourceLedger projection must be rejected, got %#v", got)
	}

	aliased := `package runtimebundle
type LedgerBag = ResourceLedger
type ResourceLedger struct{}
func (l *LedgerBag) ExportClosers() []func() error { return nil }
func exportAliased(l *LedgerBag) []func() error { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_alias.go", aliased)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "method:LedgerBag.ExportClosers") &&
		!findingsContainIdentity(got, "method:ResourceLedger.ExportClosers") {
		t.Fatalf("aliased ResourceLedger method projection must be rejected, got %#v", got)
	}
	if !findingsContainIdentity(got, "func:exportAliased") {
		t.Fatalf("aliased ResourceLedger top-level projection must be rejected, got %#v", got)
	}

	unrelatedRecv := `package runtimebundle
func (c *CandidateRuntime) ExportClosers() []func() error { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_candidate.go", unrelatedRecv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("non-ResourceLedger receiver with non-LegacyClosers name must not be flagged, got %#v", got)
	}

	localSliceHelper := `package runtimebundle
func disposeLocal(closers []func() error) error { return nil }
func withDisposed(err error, closers []func() error) error { return err }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/build_lifecycle.go", localSliceHelper)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("local closer-slice helpers without ResourceLedger must not be flagged, got %#v", got)
	}

	constructionThread := `package runtimebundle
func registerStartedCatalogClosers(ledger *ResourceLedger, closers []func() error, started any) []func() error {
	return closers
}
func appendBackendClosers(closers []func() error, cfg any, backends any, ledger *ResourceLedger) []func() error {
	return closers
}
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/build_model.go", constructionThread)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("construction helpers threading ledger+closer slice must not be flagged, got %#v", got)
	}

	processCleanup := `package runtimebundle
type ProcessServices struct{}
func (ps *ProcessServices) Close() error { return nil }
func DisposeProcessClosersForTest(closers []func() error) error { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/process_services.go", processCleanup)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("ProcessServices cleanup ownership must not be flagged, got %#v", got)
	}

	resultAlias := `package runtimebundle
type CloseFn = func() error
type ResourceLedger struct{}
func project(l *ResourceLedger) []CloseFn { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_result_alias.go", resultAlias)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:project") {
		t.Fatalf("[]CloseFn result alias projection must be rejected, got %#v", got)
	}

	resultSliceAlias := `package runtimebundle
type CloseFn = func() error
type CloseBag = []CloseFn
type ResourceLedger struct{}
func project2(l *ResourceLedger) CloseBag { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_result_slice_alias.go", resultSliceAlias)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:project2") {
		t.Fatalf("CloseBag result alias projection must be rejected, got %#v", got)
	}

	definedResult := `package runtimebundle
type DefinedCloseFn func() error
type DefinedCloseBag []DefinedCloseFn
type ResourceLedger struct{}
func (l *ResourceLedger) ExportDefined() DefinedCloseBag { return nil }
`
	got, err = scanTask42LedgerCloserProjectionSource("internal/infra/runtimebundle/sneak_defined_result.go", definedResult)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "method:ResourceLedger.ExportDefined") {
		t.Fatalf("DefinedCloseBag ResourceLedger method result must be rejected, got %#v", got)
	}
}

func TestTask42Detector_TestCtorInProductionFile(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
func NewCandidateRuntimeForTest(ledger *ResourceLedger) *CandidateRuntime { return nil }
`
	got, err := scanTask42TestCtorInProductionSource("internal/infra/runtimebundle/candidate_lifecycle.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:NewCandidateRuntimeForTest") {
		t.Fatalf("expected production-file ForTest constructor finding, got %#v", got)
	}

	inTest, err := scanTask42TestCtorInProductionSource("internal/infra/runtimebundle/export_test.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(inTest) != 0 {
		t.Fatalf("export_test.go (a _test.go file) must be excluded, got %#v", inTest)
	}
}
