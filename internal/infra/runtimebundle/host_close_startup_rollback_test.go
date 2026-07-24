package runtimebundle

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// Task 7.4: after bindReloadHost returns a complete Host, every remaining
// startup failure must roll back through exactly one Host.Close rather than
// decomposing Manager / ProcessServices / tracing shutdown primitives.

// TestHostBuild_PostBindRollbackUsesOneHostClose proves the post-bind
// coordinator-probe failure path closes the complete Host once and keeps the
// stage-probe cleaned accounting intact.
func TestHostBuild_PostBindRollbackUsesOneHostClose(t *testing.T) {
	t.Parallel()
	in := hostBuildInput{
		ConfigPath:      filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}
	out, err := productionHostBuilder{}.BuildFaulting(context.Background(), in, hostBuildStageCoordinator)
	if err == nil {
		cleanupReloadHost(t, out.Host)
		t.Fatal("expected coordinator stage failure")
	}
	if out.Host != nil {
		t.Fatal("post-bind rollback must return nil Host")
	}
	wantCleaned := "coordinator,publish,compile,process,tracing"
	if got := strings.Join(out.Journal.Cleaned, ","); got != wantCleaned {
		t.Fatalf("cleaned=%s want %s", got, wantCleaned)
	}
}

// TestHostBuild_RollbackSourceHasNoHostDecomposition locks the post-bind
// rollback to the single Host.Close seam at the source level.
func TestHostBuild_RollbackSourceHasNoHostDecomposition(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("host_build.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "host_build.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var closeCalls int
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "buildHostWithEnv" || fd.Body == nil {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			base, ok := sel.X.(*ast.Ident)
			if !ok || base.Name != "host" {
				return true
			}
			switch sel.Sel.Name {
			case "Close":
				closeCalls++
			case "Manager", "Process", "ShutdownTracing", "Coordinator":
				t.Fatalf("post-bind rollback must not decompose Host field %s", sel.Sel.Name)
			}
			return true
		})
		return false
	})
	if closeCalls != 1 {
		t.Fatalf("buildHostWithEnv host.Close calls=%d want exactly 1", closeCalls)
	}
}

// TestHostClose_HTTPHandlerIsStableDataPlaneSeam proves the host exposes one
// stable dispatcher for the serving adapter instead of handing out its Manager.
func TestHostClose_HTTPHandlerIsStableDataPlaneSeam(t *testing.T) {
	t.Parallel()
	host, err := BuildHost(context.Background(), BuildHostInput{
		ConfigPath:      filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { cleanupReloadHost(t, host) })

	first := host.HTTPHandler()
	if first == nil {
		t.Fatal("HTTPHandler must expose the stable generation dispatcher")
	}
	if second := host.HTTPHandler(); second != first {
		t.Fatal("HTTPHandler must return one long-lived dispatcher")
	}
	var _ http.Handler = first
}
