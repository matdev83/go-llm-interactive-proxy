package lipruntime_test

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase55_FacadeExposesExecutableGenerationWithoutInternalTypes(t *testing.T) {
	t.Parallel()
	assertLipruntimeImportsPublicOnly(t)
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID: "facade-req", Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: facadePhase55Req{},
		}},
		RaterRegistrations: []economics.RaterRegistration{{
			ID: "facade-rater", Perspective: metering.PerspectiveOperator, Rater: facadePhase55Rater{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if rt.ExecutableGenerationID() == 0 {
		t.Fatal("expected executable generation id")
	}
	if v := rt.ExecutableGenerationVersion(); v == "" {
		t.Fatal("expected executable generation version")
	}
	if rt.ExecutableGenerationState() != cp.CapabilityReady && string(rt.ExecutableGenerationState()) != "ready" {
		t.Fatalf("state=%q", rt.ExecutableGenerationState())
	}
	if rt.ExecutableEvidenceObjectID() != "facade-rater" {
		t.Fatalf("evidence=%q want facade-rater", rt.ExecutableEvidenceObjectID())
	}
	if rt.SnapshotGenerationID() == 0 {
		t.Fatal("compatibility metadata SnapshotGenerationID must remain")
	}
	report := rt.ReadinessReport()
	if report == nil {
		t.Fatal("readiness report")
	}
	got, err := report.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutableGeneration.EvidenceObjectID != "facade-rater" {
		t.Fatalf("report evidence=%q", got.ExecutableGeneration.EvidenceObjectID)
	}
}

func TestPhase55_DocDistinguishesExecutableFromMetadataPublication(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	docPath := filepath.Join(root, "pkg", "lipruntime", "doc.go")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, needle := range []string{"executable generation", "metadata", "compatibility"} {
		if !strings.Contains(strings.ToLower(text), needle) {
			t.Fatalf("doc.go missing %q guidance", needle)
		}
	}
	if strings.Contains(text, "authoritycoord") || strings.Contains(text, "RequestCoordinator") {
		t.Fatal("public docs must not expose internal coordinator types")
	}
}

func assertLipruntimeImportsPublicOnly(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(path, "authoritycoord") || strings.Contains(path, "internal/core/runtime") {
					t.Fatalf("lipruntime must not import %s", path)
				}
			}
		}
	}
}

type facadePhase55Req struct{}

func (facadePhase55Req) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: "facade-req", Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}
func (facadePhase55Req) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}
func (facadePhase55Req) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}
func (facadePhase55Req) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

type facadePhase55Rater struct{}

func (facadePhase55Rater) Rate(context.Context, economics.RatingRequest) (economics.RatingResult, error) {
	return economics.RatingResult{RaterID: "facade-rater"}, nil
}
