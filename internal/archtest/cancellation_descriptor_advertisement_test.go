package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// eligibleDescriptorServices lists first-party connectors whose Execute path uses
// backendplugin.ForwardExecute (directly or via connector-support wrapper).
// localstub implements a custom Execute loop and must NOT advertise the handshake.
var eligibleDescriptorServices = []string{
	"connectors/acp/internal/service/service.go",
	"connectors/agycliacp/internal/service/service.go",
	"connectors/codex/internal/service/service.go",
	"connectors/commandcode-anthropic/internal/service/service.go",
	"connectors/commandcode-openai/internal/service/service.go",
	"connectors/cursorcliacp/internal/service/service.go",
	"connectors/cursorsdk/internal/service/service.go",
	"connectors/geminicliacp/internal/service/service.go",
	"connectors/huggingface/internal/service/service.go",
	"connectors/llamacpp/internal/service/service.go",
	"connectors/lmstudio/internal/service/service.go",
	"connectors/nvidia/internal/service/service.go",
	"connectors/ollama/internal/service/service.go",
	"connectors/opencode/internal/service/service.go",
	"connectors/openrouter/internal/service/service.go",
	"connectors/vllm/internal/service/service.go",
}

var excludedDescriptorServices = []string{
	"connectors/localstub/internal/service/service.go",
}

func TestArch_CancellationHandshake_DescriptorAdvertisement(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	for _, rel := range eligibleDescriptorServices {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, filepath.FromSlash(rel))
			dir := filepath.Dir(path)
			if !dirHasForwardExecuteAST(t, dir) {
				t.Fatalf("%s: eligible connector must use ForwardExecute (direct or openaicompat wrapper)", rel)
			}
			if !dirHasIdentAST(t, dir, "ProtocolMinorCancellationHandshake") {
				t.Fatalf("%s: must advertise ProtocolMinorCancellationHandshake (minor 8)", rel)
			}
			if !dirHasIdentAST(t, dir, "FeatureCancellationHandshake") {
				t.Fatalf("%s: must advertise FeatureCancellationHandshake", rel)
			}
			if dirHasFeatureCancellationHandshakeRequiredTrueAST(t, dir) {
				t.Fatalf("%s: FeatureCancellationHandshake must be optional (Required false)", rel)
			}
		})
	}

	for _, rel := range excludedDescriptorServices {
		t.Run("excluded/"+rel, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, filepath.FromSlash(rel))
			dir := filepath.Dir(path)
			if dirHasIdentAST(t, dir, "FeatureCancellationHandshake") {
				t.Fatalf("%s: excluded connector (custom Execute) must NOT advertise FeatureCancellationHandshake", rel)
			}
		})
	}
}

func TestArch_CancellationHandshake_CodexPreservesExistingFeatures(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connectors/codex/internal/service")
	required := []string{
		"FeatureExactReasoningParts",
		"FeatureAccountingEvidence",
		"FeatureOrderedItems",
		"FeatureExactOpenResponsesFields",
		"FeatureProxyOwnedSessionID",
	}
	for _, feat := range required {
		if !dirHasIdentAST(t, dir, feat) {
			t.Fatalf("codex descriptor must preserve %s", feat)
		}
	}
	if !dirHasIdentAST(t, dir, "ProtocolMinorCancellationHandshake") {
		t.Fatalf("codex descriptor must be upgraded to ProtocolMinorCancellationHandshake")
	}
	if !dirHasIdentAST(t, dir, "FeatureCancellationHandshake") {
		t.Fatalf("codex descriptor must include FeatureCancellationHandshake")
	}
}

func dirHasForwardExecuteAST(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		if fileHasForwardExecuteAST(t, path) {
			return true
		}
	}
	return false
}

func fileHasForwardExecuteAST(t *testing.T, path string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "ForwardExecute" {
			found = true
			return false
		}
		return true
	})
	return found
}

func dirHasIdentAST(t *testing.T, dir, ident string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		if fileHasIdentAST(t, path, ident) {
			return true
		}
	}
	return false
}

func fileHasIdentAST(t *testing.T, path, ident string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == ident {
			found = true
			return false
		}
		return true
	})
	return found
}

func dirHasFeatureCancellationHandshakeRequiredTrueAST(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		if fileHasFeatureCancellationHandshakeRequiredTrueAST(t, path) {
			return true
		}
	}
	return false
}

func fileHasFeatureCancellationHandshakeRequiredTrueAST(t *testing.T, path string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	viol := false
	ast.Inspect(file, func(n ast.Node) bool {
		if viol {
			return false
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var hasName, hasRequiredTrue bool
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			keyIdent, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch keyIdent.Name {
			case "Name":
				if strings.Contains(nodeText(kv.Value), "FeatureCancellationHandshake") {
					hasName = true
				}
			case "Required":
				if id, ok := kv.Value.(*ast.Ident); ok && id.Name == "true" {
					hasRequiredTrue = true
				}
			}
		}
		if hasName && hasRequiredTrue {
			viol = true
			return false
		}
		return true
	})
	return viol
}
