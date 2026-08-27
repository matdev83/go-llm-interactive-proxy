package archtest

import (
	"go/parser"
	"go/token"
	"testing"
)

// scanSyntheticSource parses src and runs ScanFileForForbiddenMirrors.
func scanSyntheticSource(t *testing.T, relPath, src string, wave MigrationWave) []MirrorFinding {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, relPath, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse synthetic source %s: %v", relPath, err)
	}
	return ScanFileForForbiddenMirrors(relPath, []byte(src), fset, f, wave)
}

// TestForbiddenMirrorPredicate_ObserverStageConsumersAllowlistAndSpoofing verifies that exact
// stage consumers (e.g. internal/infra/runtimebundle.buildRuntimeSnapshot) are allowed, while
// unauthorized or spoofed functions reading observer planes via Get are strictly rejected under Wave 2.
func TestForbiddenMirrorPredicate_ObserverStageConsumersAllowlistAndSpoofing(t *testing.T) {
	t.Parallel()

	// 1. Unauthorized function reading PlaneTrafficObservers via Get is REJECTED
	unauthorizedSrc := `package runtimebundle
import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

func unauthorizedObserverConsumer(f lipfeature.FrozenPlaneSet) []traffic.Observer {
	return lipfeature.Get(f, lipfeature.PlaneTrafficObservers)
}
`
	findingsUnauth := scanSyntheticSource(t, "internal/infra/runtimebundle/unauthorized.go", unauthorizedSrc, Wave2_Observers)
	if len(findingsUnauth) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for unauthorizedObserverConsumer at Wave2")
	}
	if findingsUnauth[0].ShapeKind != MirrorStageConsumer || findingsUnauth[0].PlaneID != "traffic_observers" {
		t.Fatalf("unexpected finding: %+v", findingsUnauth[0])
	}

	// 2. Spoofed variants (e.g. buildRuntimeSnapshot2, buildRuntimeSnapshot_evil) are REJECTED
	spoofVariants := []struct {
		name     string
		funcName string
	}{
		{name: "numbered spoof", funcName: "buildRuntimeSnapshot2"},
		{name: "suffix spoof", funcName: "buildRuntimeSnapshot_evil"},
	}

	for _, tc := range spoofVariants {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := `package runtimebundle
import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

func ` + tc.funcName + `(f lipfeature.FrozenPlaneSet) []traffic.Observer {
	return lipfeature.Get(f, lipfeature.PlaneTrafficObservers)
}
`
			findings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_extension.go", src, Wave2_Observers)
			if len(findings) == 0 {
				t.Fatalf("expected forbidden stage consumer finding for spoofed %s (%s) at Wave2", tc.name, tc.funcName)
			}
			if findings[0].ShapeKind != MirrorStageConsumer || findings[0].PlaneID != "traffic_observers" {
				t.Fatalf("unexpected finding for %s: %+v", tc.funcName, findings[0])
			}
		})
	}

	// 3. Legitimate exact allowlist function (buildRuntimeSnapshot) is ALLOWED
	allowedSrc := `package runtimebundle
import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

func buildRuntimeSnapshot(f lipfeature.FrozenPlaneSet) []traffic.Observer {
	return lipfeature.Get(f, lipfeature.PlaneTrafficObservers)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_extension.go", allowedSrc, Wave2_Observers)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed exact buildRuntimeSnapshot function, got: %+v", allowedFindings)
	}

	// 4. Same-name function in foreign package (foreign spoof) is REJECTED
	foreignFindings := scanSyntheticSource(t, "internal/foreign/spoof.go", allowedSrc, Wave2_Observers)
	if len(foreignFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for foreign same-name spoof buildRuntimeSnapshot at Wave2")
	}
	if foreignFindings[0].ShapeKind != MirrorStageConsumer || foreignFindings[0].PlaneID != "traffic_observers" {
		t.Fatalf("unexpected finding for foreign spoof: %+v", foreignFindings[0])
	}
}

// TestForbiddenMirrorPredicate_ForeignHookProjectionSpoofRejected verifies that a same-name
// foreign function attempting to project hooks via Get is strictly rejected under Wave 1.
func TestForbiddenMirrorPredicate_ForeignHookProjectionSpoofRejected(t *testing.T) {
	t.Parallel()

	foreignHookSrc := `package foreign
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func HooksConfigFromFrozen(f lipfeature.FrozenPlaneSet, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return hooks.Config{
		SubmitHooks: lipfeature.Get(f, lipfeature.PlaneSubmitHooks),
	}
}
`
	foreignFindings := scanSyntheticSource(t, "internal/foreign/fake_hooks.go", foreignHookSrc, Wave1_HookBus)
	if len(foreignFindings) == 0 {
		t.Fatalf("expected forbidden finding for foreign same-name spoof HooksConfigFromFrozen at Wave1")
	}
}
