package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestForbiddenMirrorPredicate_DiagnosticsArms verifies that switch/if arms in
// diagnostics keyed to specific plane fields are rejected, while catalog iteration passes.
func TestForbiddenMirrorPredicate_DiagnosticsArms(t *testing.T) {
	t.Parallel()

	// 1. If statement on plane field is rejected
	forbiddenIfSrc := `package diag
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"

func stageOccupancyFromBundle(b lipfeature.FeatureBundle) []InventoryStageOccupancy {
	out := make([]InventoryStageOccupancy, 0, 4)
	if n := len(b.ToolCatalogFilters); n > 0 {
		out = append(out, InventoryStageOccupancy{Count: n})
	}
	return out
}
`
	findingsIf := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", forbiddenIfSrc, Wave5c_Residual)
	if len(findingsIf) == 0 {
		t.Fatalf("expected forbidden diagnostics arm finding for ToolCatalogFilters at Wave5c (if stmt)")
	}
	if findingsIf[0].ShapeKind != MirrorDiagArm || findingsIf[0].PlaneID != "tool_catalog_filters" {
		t.Fatalf("unexpected finding for if stmt: %+v", findingsIf[0])
	}
	// At earlier waves (Wave1-Wave5b), diagnostics field reads from FeatureBundle are allowed until Wave5c
	findingsIfW1 := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", forbiddenIfSrc, Wave1_HookBus)
	if len(findingsIfW1) != 0 {
		t.Fatalf("expected 0 findings at Wave1 for diagnostics arm, got %+v", findingsIfW1)
	}

	// 2. Switch statement arm on plane field is rejected
	forbiddenSwitchSrc := `package diag
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"

func stageOccupancyFromBundleSwitch(b lipfeature.FeatureBundle, stage string) int {
	switch stage {
	case "tools":
		return len(b.ToolCatalogFilters)
	default:
		return 0
	}
}
`
	findingsSwitch := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", forbiddenSwitchSrc, Wave5c_Residual)
	if len(findingsSwitch) == 0 {
		t.Fatalf("expected forbidden diagnostics switch arm finding for ToolCatalogFilters at Wave5c (switch stmt)")
	}
	if findingsSwitch[0].ShapeKind != MirrorDiagArm || findingsSwitch[0].PlaneID != "tool_catalog_filters" {
		t.Fatalf("unexpected finding for switch stmt: %+v", findingsSwitch[0])
	}

	// 3. Catalog iteration passes
	allowedSrc := `package diag
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"

func stageOccupancyFromFrozen(frozen lipfeature.FrozenPlaneSet) []InventoryStageOccupancy {
	out := make([]InventoryStageOccupancy, 0)
	for _, p := range lipfeature.StandardPlanes {
		out = append(out, InventoryStageOccupancy{Count: 1})
	}
	return out
}
`
	allowedFindings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for catalog iteration diagnostics, got %+v", allowedFindings)
	}
}

// TestForbiddenMirrorPredicate_StageConsumers verifies that stage consumer accessors
// outside AllowedStageConsumers or failing thin-delegation are rejected past their wave,
// while whitelisted thin-delegating stage consumers pass.
func TestForbiddenMirrorPredicate_StageConsumers(t *testing.T) {
	t.Parallel()

	// 1. Allowed explicit stage consumer in internal/core/extensions delegating to Get passes at Wave3
	allowedConsumerSrc := `package extensions
import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

type RequestRuntimeSnapshot struct {
	frozen lipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) CompletionGates() []completion.Gate {
	return lipfeature.Get(s.frozen, lipfeature.PlaneCompletionGates)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/core/extensions/seam_views.go", allowedConsumerSrc, Wave3_RequestShaping)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed thin-delegating stage consumer, got %+v", allowedFindings)
	}

	// 2. Unregistered/non-whitelisted stage consumer is rejected at Wave3
	unregisteredConsumerSrc := `package custom
import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

type UnregisteredConsumer struct {
	frozen lipfeature.FrozenPlaneSet
}

func (u *UnregisteredConsumer) CompletionGates() []completion.Gate {
	return lipfeature.Get(u.frozen, lipfeature.PlaneCompletionGates)
}
`
	unregisteredFindings := scanSyntheticSource(t, "internal/custom/consumer.go", unregisteredConsumerSrc, Wave3_RequestShaping)
	if len(unregisteredFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for non-whitelisted symbol at Wave3")
	}
	if unregisteredFindings[0].ShapeKind != MirrorStageConsumer || unregisteredFindings[0].PlaneID != "completion_gates" {
		t.Fatalf("unexpected finding: %+v", unregisteredFindings[0])
	}
	if !strings.Contains(unregisteredFindings[0].Detail, "not in AllowedStageConsumers allowlist") {
		t.Fatalf("expected detail to mention AllowedStageConsumers, got %q", unregisteredFindings[0].Detail)
	}

	// 3. Whitelisted stage consumer that is not a thin delegate (e.g. extra branching/statements) is rejected at Wave3
	nonThinConsumerSrc := `package extensions
import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

type RequestRuntimeSnapshot struct {
	frozen lipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) CompletionGates() []completion.Gate {
	if s == nil {
		return nil
	}
	return lipfeature.Get(s.frozen, lipfeature.PlaneCompletionGates)
}
`
	nonThinFindings := scanSyntheticSource(t, "internal/core/extensions/seam_views.go", nonThinConsumerSrc, Wave3_RequestShaping)
	if len(nonThinFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for non-thin delegate at Wave3")
	}
	if nonThinFindings[0].ShapeKind != MirrorStageConsumer || nonThinFindings[0].PlaneID != "completion_gates" {
		t.Fatalf("unexpected finding: %+v", nonThinFindings[0])
	}
	if !strings.Contains(nonThinFindings[0].Detail, "does not thinly delegate to Get") {
		t.Fatalf("expected detail to mention non-thin delegation, got %q", nonThinFindings[0].Detail)
	}

	// 4. EvilCompletionGates calling lipfeature.Get outside whitelist is rejected at Wave3 (Finding 3 fixture a)
	evilSrc := `package custom
import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

type FakeSnapshot struct {
	frozen lipfeature.FrozenPlaneSet
}

func EvilCompletionGates(s *FakeSnapshot) []completion.Gate {
	return lipfeature.Get(s.frozen, lipfeature.PlaneCompletionGates)
}
`
	evilFindings := scanSyntheticSource(t, "internal/custom/evil.go", evilSrc, Wave3_RequestShaping)
	if len(evilFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for EvilCompletionGates at Wave3")
	}
	if evilFindings[0].ShapeKind != MirrorStageConsumer || evilFindings[0].PlaneID != "completion_gates" {
		t.Fatalf("unexpected finding for EvilCompletionGates: %+v", evilFindings[0])
	}
	if !strings.Contains(evilFindings[0].Detail, "not in AllowedStageConsumers allowlist") {
		t.Fatalf("expected detail to mention AllowedStageConsumers, got %q", evilFindings[0].Detail)
	}

	// 5. Singular named method TrafficObserver outside whitelist is rejected at Wave2
	evilTrafficSrc := `package custom
import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

type CustomSnapshot struct {
	frozen lipfeature.FrozenPlaneSet
}

func (c *CustomSnapshot) TrafficObserver() traffic.Observer {
	return lipfeature.Get(c.frozen, lipfeature.PlaneTrafficObservers)
}
`
	evilTrafficFindings := scanSyntheticSource(t, "internal/custom/traffic.go", evilTrafficSrc, Wave2_Observers)
	if len(evilTrafficFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for CustomSnapshot.TrafficObserver at Wave2")
	}
	if evilTrafficFindings[0].ShapeKind != MirrorStageConsumer || evilTrafficFindings[0].PlaneID != "traffic_observers" {
		t.Fatalf("unexpected finding: %+v", evilTrafficFindings[0])
	}
}

// TestIsThinDelegate verifies AST inspection for strictly thin delegates.
func TestIsThinDelegate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
		src  string
		want bool
	}{
		{
			name: "lipfeature.Get call",
			src: `package p
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
func F(s *S) []int { return lipfeature.Get(s.frozen, p) }`,
			want: true,
		},
		{
			name: "feature.Get call",
			src: `package p
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
func F(s *S) []int { return feature.Get(s.frozen, p) }`,
			want: true,
		},
		{
			name: "lipfeature.FrozenIdentity call",
			src: `package p
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
func F(s *S) string { return lipfeature.FrozenIdentity(s.frozen, p) }`,
			want: true,
		},
		{
			name: "bare Get call in feature package",
			path: "pkg/lipsdk/feature/test.go",
			src: `package feature
func F(s *S) []int { return Get(s.frozen, p) }`,
			want: true,
		},
		{
			name: "bare Get call in non-feature package rejected",
			src: `package p
func F(s *S) []int { return Get(s.frozen, p) }`,
			want: false,
		},
		{
			name: "foreign directory package feature bare Get rejected",
			path: "internal/custom/test.go",
			src: `package feature
func F(s *S) []int { return Get(s.frozen, p) }`,
			want: false,
		},
		{
			name: "evil import suffix spoofing rejected",
			src: `package p
import "evil.example/pkg/lipsdk/feature"
func F(s *S) []int { return feature.Get(s.frozen, p) }`,
			want: false,
		},
		{
			name: "evil import aliased as lipfeature rejected",
			src: `package p
import lipfeature "evil.example/pkg/lipsdk/feature"
func F(s *S) []int { return lipfeature.Get(s.frozen, p) }`,
			want: false,
		},
		{
			name: "other package Get call rejected",
			src: `package p
import "other/pkg"
func F(s *S) []int { return pkg.Get(s.frozen, p) }`,
			want: false,
		},
		{
			name: "other package aliased as feature rejected",
			src: `package p
import feature "other/pkg"
func F(s *S) []int { return feature.Get(s.frozen, p) }`,
			want: false,
		},
		{
			name: "extra if branch rejected",
			src: `package p
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
func F(s *S) []int {
	if s == nil { return nil }
	return lipfeature.Get(s.frozen, p)
}`,
			want: false,
		},
		{
			name: "extra statement rejected",
			src: `package p
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
func F(s *S) []int {
	var x int = 1
	_ = x
	return lipfeature.Get(s.frozen, p)
}`,
			want: false,
		},
		{
			name: "non-return body rejected",
			src: `package p
func F(s *S) {
	for {}
}`,
			want: false,
		},
		{
			name: "multiple return values rejected",
			src: `package p
import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
func F(s *S) ([]int, error) {
	return lipfeature.Get(s.frozen, p), nil
}`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filePath := tc.path
			if filePath == "" {
				filePath = "test.go"
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, filePath, tc.src, 0)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			var fnDecl *ast.FuncDecl
			for _, decl := range f.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					fnDecl = fd
					break
				}
			}
			if fnDecl == nil {
				t.Fatalf("no function declaration found in %s", tc.src)
			}
			got := IsThinDelegate(filePath, fnDecl, f)
			if got != tc.want {
				t.Errorf("IsThinDelegate() = %v, want %v", got, tc.want)
			}
		})
	}
}
