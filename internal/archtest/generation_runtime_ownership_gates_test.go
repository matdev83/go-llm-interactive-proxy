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
type generationExecution struct{ executor any }
type generationHTTPPublication struct{ handler any }
type generationModelViews struct{}
type generationOperations struct{}
type generationOwnership struct{ ledger any }
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
	ownership   generationOwnership
}
`
	got, err := scanGenerationRuntimeOwnershipSource("internal/infra/runtimebundle/synthetic_clean.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 0 {
		t.Fatalf("clean grouped shape must have no findings, got %#v", got.Findings)
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
	ownership   struct{}
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

func ownershipFindingsContainKind(fs []generationRuntimeOwnershipFinding, kind string) bool {
	for _, f := range fs {
		if f.Kind == kind {
			return true
		}
	}
	return false
}
