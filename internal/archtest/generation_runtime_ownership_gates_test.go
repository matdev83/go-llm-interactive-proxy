package archtest

import (
	"strings"
	"testing"
)

func TestGenerationRuntime_OwnershipArchitectureGate(t *testing.T) {
	t.Parallel()
	got := scanRuntimebundleGenerationOwnership(t)
	if len(got.Findings) > 0 {
		var b strings.Builder
		for _, f := range got.Findings {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("GenerationRuntime ownership architecture findings:\n%s", b.String())
	}
}

func TestGenerationRuntime_SyntheticGenerationOwnerDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type generationOwner interface {
	Close() error
}
type GenerationBundle struct {
	owner generationOwner
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_owner.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "generation_owner_delegate") &&
		!ownershipFindingsContainKind(got.Findings, "candidate_owner_field") {
		t.Fatalf("expected generationOwner detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticCandidateRuntimeFieldDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type CandidateRuntime struct{}
type GenerationBundle struct {
	cand *CandidateRuntime
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_cand.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "candidate_owner_field") {
		t.Fatalf("expected CandidateRuntime field detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticAliasCandidateDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type CandidateRuntime struct{}
type OwnerHandle = *CandidateRuntime
type GenerationBundle struct {
	held OwnerHandle
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_alias_cand.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "candidate_owner_field") {
		t.Fatalf("expected alias CandidateRuntime detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticEmbeddedCandidateDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type CandidateRuntime struct{}
type GenerationBundle struct {
	CandidateRuntime
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_embed.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "candidate_owner_field") {
		t.Fatalf("expected embedded CandidateRuntime detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticGenericLookupDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type GenerationRuntime interface {
	Lookup(name string) any
}
type GenerationBundle struct{}
func (b *GenerationBundle) Get(name string) any { return nil }
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_lookup.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "generic_dependency_lookup") {
		t.Fatalf("expected generic lookup detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticDualOnceDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
import "sync"
type GenerationBundle struct {
	quiesceOnce sync.Once
	closeOnce   sync.Once
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_once.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "dual_once_lifecycle") {
		t.Fatalf("expected dual Once detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticMutableConfigDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type Config struct{}
type GenerationBundle struct {
	cfg *Config
	Built any
	requestPlane any
	ProcessServices any
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_mutable.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "mutable_or_process_owner_field") {
		t.Fatalf("expected mutable/process owner detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticGroupedShapeClean(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type generationExecution struct{ executor any }
type generationHTTPPublication struct{ handler any }
type generationModelViews struct{}
type generationOperations struct{}
type GenerationRuntime interface {
	Handler() any
	ExecutorView() any
	BindModelViews(ctx any) any
	BackendFactoryKindCounts() map[string]int
	TerminalProviders() any
	ReadinessReport() any
	Quiesce(ctx any) error
	Close() error
}
type GenerationBundle struct {
	execution   generationExecution
	publication generationHTTPPublication
	models      generationModelViews
	operations  generationOperations
	ledger      *ResourceLedger
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_clean.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 0 {
		t.Fatalf("clean grouped shape must have no findings, got %#v", got.Findings)
	}
	if !got.HasCanonicalLedger {
		t.Fatal("clean shape must record canonical ledger field")
	}
	for _, g := range requiredGenerationRuntimeGroups {
		if !got.GroupFields[g] {
			t.Fatalf("missing group %q", g)
		}
	}
}

// Precision controls: unrelated Get/Lookup outside generation-runtime ownership
// must not be falsely rejected by the scanner.
func TestGenerationRuntime_PrecisionUnrelatedGetLookupNotRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type WidgetCache struct{}
func (w *WidgetCache) Get(key string) any { return nil }
func (w *WidgetCache) Lookup(key string) any { return nil }
func (w *WidgetCache) TransferLedgerOwnership() *ResourceLedger { return nil }
func (w *WidgetCache) GetLedger() *ResourceLedger { return nil }
type LocalExecutorProvider struct{}
func (l *LocalExecutorProvider) Resolve() any { return nil }
type GenerationRuntime interface {
	Handler() any
}
type GenerationBundle struct {
	execution   struct{}
	publication struct{}
	models      struct{}
	operations  struct{}
	ledger      *ResourceLedger
}
type CandidateRuntime struct{}
func (c *CandidateRuntime) Quiesce() error { return nil }
func (c *CandidateRuntime) Close() error { return nil }
func (c *CandidateRuntime) transferLedgerOwnership() *ResourceLedger { return nil }
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_precision.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if ownershipFindingsContainKind(got.Findings, "generic_dependency_lookup") {
		t.Fatalf("unrelated Get/Lookup/Resolve must not be flagged, got %#v", got.Findings)
	}
	if ownershipFindingsContainKind(got.Findings, "exported_ownership_transfer") {
		t.Fatalf("unrelated/package-private transfer must not be flagged, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticExportedTransferDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type CandidateRuntime struct{}
func (c *CandidateRuntime) TransferLedgerOwnership() *ResourceLedger { return nil }
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_transfer.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "exported_ownership_transfer") {
		t.Fatalf("expected exported ownership transfer detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticExportedLedgerGetterDetected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type GenerationBundle struct{}
func (b *GenerationBundle) GetLedger() *ResourceLedger { return nil }
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_ledger_getter.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "exported_ownership_transfer") {
		t.Fatalf("expected exported ledger getter detection, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticCanonicalLedgerAliasAccepted(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type LedgerPtr = *ResourceLedger
type NestedLedgerAlias = LedgerPtr
type generationExecution struct{ executor any }
type generationHTTPPublication struct{ handler any }
type generationModelViews struct{}
type generationOperations struct{}
type GenerationRuntime interface {
	Handler() any
}
type GenerationBundle struct {
	execution   generationExecution
	publication generationHTTPPublication
	models      generationModelViews
	operations  generationOperations
	ledger      NestedLedgerAlias
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_ledger_alias.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 0 {
		t.Fatalf("canonical alias ledger must be clean, got %#v", got.Findings)
	}
	if !got.HasCanonicalLedger {
		t.Fatal("alias to *ResourceLedger must count as canonical ledger")
	}
}

func TestGenerationRuntime_SyntheticFakeResourceLedgerRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type FakeResourceLedger struct{}
type GenerationBundle struct {
	execution   struct{}
	publication struct{}
	models      struct{}
	operations  struct{}
	ledger      *FakeResourceLedger
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_fake_ledger.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasCanonicalLedger {
		t.Fatal("FakeResourceLedger must not satisfy canonical ledger gate")
	}
	if !ownershipFindingsContainKind(got.Findings, "non_canonical_ledger") {
		t.Fatalf("expected non_canonical_ledger, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticAlternateResourceLedgerRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type AlternateResourceLedger struct{}
type GenerationBundle struct {
	execution   struct{}
	publication struct{}
	models      struct{}
	operations  struct{}
	ledger      *AlternateResourceLedger
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_alt_ledger.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasCanonicalLedger || !ownershipFindingsContainKind(got.Findings, "non_canonical_ledger") {
		t.Fatalf("AlternateResourceLedger must be rejected, got has=%v findings=%#v", got.HasCanonicalLedger, got.Findings)
	}
}

func TestGenerationRuntime_SyntheticInterfaceLedgerRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type ResourceLedgerOwner interface{ Close() error }
type GenerationBundle struct {
	execution   struct{}
	publication struct{}
	models      struct{}
	operations  struct{}
	ledger      ResourceLedgerOwner
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_iface_ledger.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasCanonicalLedger || !ownershipFindingsContainKind(got.Findings, "non_canonical_ledger") {
		t.Fatalf("interface ledger owner must be rejected, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticSecondLedgerFieldRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type GenerationBundle struct {
	execution   struct{}
	publication struct{}
	models      struct{}
	operations  struct{}
	ledger      *ResourceLedger
	extra       *ResourceLedger
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_dup_ledger.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "duplicate_canonical_ledger") {
		t.Fatalf("expected duplicate_canonical_ledger, got %#v", got.Findings)
	}
	if got.HasCanonicalLedger {
		t.Fatal("duplicate ledger fields must not set HasCanonicalLedger")
	}
}

func TestGenerationRuntime_SyntheticEmbeddedLedgerRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type GenerationBundle struct {
	execution   struct{}
	publication struct{}
	models      struct{}
	operations  struct{}
	*ResourceLedger
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_embed_ledger.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "nested_ledger_owner") {
		t.Fatalf("expected nested_ledger_owner for embed, got %#v", got.Findings)
	}
	if got.HasCanonicalLedger {
		t.Fatal("embedded ledger must not count as canonical field")
	}
}

func TestGenerationRuntime_SyntheticNestedLedgerShellRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type ledgerShell struct{ inner *ResourceLedger }
type GenerationBundle struct {
	execution   struct{}
	publication struct{}
	models      struct{}
	operations  struct{}
	wrap        ledgerShell
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_nested_ledger.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !ownershipFindingsContainKind(got.Findings, "nested_ledger_owner") {
		t.Fatalf("expected nested_ledger_owner, got %#v", got.Findings)
	}
	if got.HasCanonicalLedger {
		t.Fatal("nested shell must not count as canonical ledger field")
	}
}

func TestGenerationRuntime_SyntheticAliasHidingLedgerRejected(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type FakeResourceLedger struct{}
type Hidden = *FakeResourceLedger
type GenerationBundle struct {
	execution   struct{}
	publication struct{}
	models      struct{}
	operations  struct{}
	ledger      Hidden
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_hidden_ledger.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasCanonicalLedger || !ownershipFindingsContainKind(got.Findings, "non_canonical_ledger") {
		t.Fatalf("alias to FakeResourceLedger must be rejected, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SyntheticUnrelatedNestedGroupsStillClean(t *testing.T) {
	t.Parallel()
	src := `package runtimebundle
type ResourceLedger struct{}
type BackendInstance struct{ closeOnce any }
type generationExecution struct {
	executor any
	local    BackendInstance
}
type generationHTTPPublication struct{ handler any }
type generationModelViews struct{}
type generationOperations struct{ readiness any }
type GenerationRuntime interface {
	Handler() any
}
type GenerationBundle struct {
	execution   generationExecution
	publication generationHTTPPublication
	models      generationModelViews
	operations  generationOperations
	ledger      *ResourceLedger
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_unrelated_nested.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 0 {
		t.Fatalf("unrelated nested groups must stay clean, got %#v", got.Findings)
	}
	if !got.HasCanonicalLedger {
		t.Fatal("canonical ledger required")
	}
}

const syntheticCleanGenerationBundleBody = `
type generationExecution struct{ executor any }
type generationHTTPPublication struct{ handler any }
type generationModelViews struct{}
type generationOperations struct{}
type GenerationRuntime interface {
	Handler() any
}
type GenerationBundle struct {
	execution   generationExecution
	publication generationHTTPPublication
	models      generationModelViews
	operations  generationOperations
	ledger      LedgerPtr
}
`

func TestGenerationRuntime_MultiFileProductionCrossFileAliasAccepted(t *testing.T) {
	t.Parallel()
	files := []ownershipScanFile{
		{
			Path: "internal/infra/runtimebundle/aliases.go",
			Src: `package runtimebundle
type ResourceLedger struct{}
type LedgerPtr = *ResourceLedger
`,
		},
		{
			Path: "internal/infra/runtimebundle/bundle.go",
			Src:  "package runtimebundle\n" + syntheticCleanGenerationBundleBody,
		},
	}
	got, err := scanGenerationRuntimeOwnershipSources(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 0 {
		t.Fatalf("production cross-file alias must be accepted, got %#v", got.Findings)
	}
	if !got.HasCanonicalLedger {
		t.Fatal("cross-file LedgerPtr must resolve to canonical *ResourceLedger")
	}
}

func TestGenerationRuntime_ExternalTestPackageCannotSatisfyProductionLedger(t *testing.T) {
	t.Parallel()
	files := []ownershipScanFile{
		{
			Path: "internal/infra/runtimebundle/bundle.go",
			Src: `package runtimebundle
` + strings.ReplaceAll(syntheticCleanGenerationBundleBody, "LedgerPtr", "Hidden"),
		},
		{
			Path: "internal/infra/runtimebundle/contaminate_test.go",
			Src: `package runtimebundle_test
type ResourceLedger struct{}
type Hidden = *ResourceLedger
type LedgerPtr = *ResourceLedger
`,
		},
	}
	got, err := scanGenerationRuntimeOwnershipSources(files)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasCanonicalLedger {
		t.Fatal("external runtimebundle_test alias must not satisfy production GenerationBundle")
	}
	if !ownershipFindingsContainKind(got.Findings, "non_canonical_ledger") &&
		!ownershipFindingsContainKind(got.Findings, "missing_canonical_ledger") {
		t.Fatalf("expected production ledger rejection without external contamination, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_SamePackageTestFileCannotSatisfyProductionLedger(t *testing.T) {
	t.Parallel()
	files := []ownershipScanFile{
		{
			Path: "internal/infra/runtimebundle/bundle.go",
			Src: `package runtimebundle
` + strings.ReplaceAll(syntheticCleanGenerationBundleBody, "LedgerPtr", "Hidden"),
		},
		{
			Path: "internal/infra/runtimebundle/alias_test.go",
			Src: `package runtimebundle
type ResourceLedger struct{}
type Hidden = *ResourceLedger
`,
		},
	}
	got, err := scanGenerationRuntimeOwnershipSources(files)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasCanonicalLedger {
		t.Fatal("same-package _test.go alias must not satisfy production GenerationBundle")
	}
	if !ownershipFindingsContainKind(got.Findings, "non_canonical_ledger") &&
		!ownershipFindingsContainKind(got.Findings, "missing_canonical_ledger") {
		t.Fatalf("expected production ledger rejection without test-file alias, got %#v", got.Findings)
	}
}

func TestGenerationRuntime_UnrelatedExternalPackageCannotCreateFindings(t *testing.T) {
	t.Parallel()
	files := []ownershipScanFile{
		{
			Path: "internal/infra/runtimebundle/aliases.go",
			Src: `package runtimebundle
type ResourceLedger struct{}
type LedgerPtr = *ResourceLedger
`,
		},
		{
			Path: "internal/infra/runtimebundle/bundle.go",
			Src:  "package runtimebundle\n" + syntheticCleanGenerationBundleBody,
		},
		{
			Path: "internal/infra/runtimebundle/otherpkg.go",
			Src: `package otherpkg
type CandidateRuntime struct{}
type GenerationBundle struct {
	cand *CandidateRuntime
	owner any
}
type GenerationRuntime interface {
	Get(name string) any
}
`,
		},
	}
	got, err := scanGenerationRuntimeOwnershipSources(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 0 {
		t.Fatalf("unrelated external package must not create production findings, got %#v", got.Findings)
	}
	if !got.HasCanonicalLedger {
		t.Fatal("production clean shape must keep canonical ledger")
	}
}

func TestGenerationRuntime_DuplicateProductionTypeFailsClosedIndependentOfOrder(t *testing.T) {
	t.Parallel()
	fileA := ownershipScanFile{
		Path: "internal/infra/runtimebundle/a.go",
		Src: `package runtimebundle
type ResourceLedger struct{}
`,
	}
	fileB := ownershipScanFile{
		Path: "internal/infra/runtimebundle/b.go",
		Src: `package runtimebundle
type ResourceLedger struct{ x int }
`,
	}
	fileBundle := ownershipScanFile{
		Path: "internal/infra/runtimebundle/bundle.go",
		Src: `package runtimebundle
type LedgerPtr = *ResourceLedger
` + syntheticCleanGenerationBundleBody,
	}
	orders := [][]ownershipScanFile{
		{fileA, fileB, fileBundle},
		{fileB, fileBundle, fileA},
		{fileBundle, fileA, fileB},
	}
	for i, files := range orders {
		got, err := scanGenerationRuntimeOwnershipSources(files)
		if err != nil {
			t.Fatalf("order %d: %v", i, err)
		}
		if !ownershipFindingsContainKind(got.Findings, "duplicate_production_type") {
			t.Fatalf("order %d: expected duplicate_production_type, got %#v", i, got.Findings)
		}
		var dup generationRuntimeOwnershipFinding
		for _, f := range got.Findings {
			if f.Kind == "duplicate_production_type" {
				dup = f
				break
			}
		}
		if !strings.Contains(dup.Detail, "ResourceLedger") {
			t.Fatalf("order %d: duplicate detail must name ResourceLedger, got %q", i, dup.Detail)
		}
		// Deterministic path order in the finding, independent of input order.
		if !strings.Contains(dup.Detail, "a.go") || !strings.Contains(dup.Detail, "b.go") {
			t.Fatalf("order %d: duplicate detail must list both paths, got %q", i, dup.Detail)
		}
		idxA := strings.Index(dup.Detail, "a.go")
		idxB := strings.Index(dup.Detail, "b.go")
		if idxA < 0 || idxB < 0 || idxA > idxB {
			t.Fatalf("order %d: duplicate paths must be sorted (a.go before b.go), got %q", i, dup.Detail)
		}
	}
}

func ownershipFindingsContainKind(fs []generationRuntimeOwnershipFinding, kind string) bool {
	for _, f := range fs {
		if f.Kind == kind {
			return true
		}
	}
	return false
}
