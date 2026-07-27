package archtest

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

func TestNoFixedOptional_MigrationScaffoldingAbsent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"internal/standardplugins/migration_bundle.go",
		"internal/pluginreg/migration_deps.go",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			t.Fatalf("Phase 8.4 must delete migration scaffolding: %s still exists", rel)
		}
	}
	src := readProductionGoSources(t, filepath.Join(root, "internal", "standardplugins"))
	for _, bad := range []string{
		"MigrationBackendBundle",
		"InstallMigrationBackendsOn",
		"migration-only",
		"MigrationBackendFactoryDeps",
	} {
		if strings.Contains(src, bad) {
			t.Fatalf("standardplugins production sources must not mention %q after Phase 8.4", bad)
		}
	}
	regSrc := readProductionGoSources(t, filepath.Join(root, "internal", "pluginreg"))
	for _, bad := range []string{"MigrationBackendFactoryDeps", "migration_deps"} {
		if strings.Contains(regSrc, bad) {
			t.Fatalf("pluginreg production sources must not mention %q after Phase 8.4", bad)
		}
	}
}

func TestNoFixedOptional_NoGeneratedBlankImportOrBuildTagLists(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "standardplugins"),
		filepath.Join(root, "internal", "pluginreg"),
		filepath.Join(root, "cmd", "lipstd"),
	}
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, ent := range ents {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, ent.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			for _, bad := range []string{
				"//go:build optional",
				"//go:build connector",
				"//go:build plugin",
				"generated import",
				"blank-import list",
				"optionalCompatibility",
				"OptionalCompatibility",
				"compatibility switch",
			} {
				if strings.Contains(text, bad) {
					t.Errorf("%s contains forbidden optional-registration scaffolding %q", filepath.Base(path), bad)
				}
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, raw, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range file.Imports {
				pathLit := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(pathLit, "/connectors/") || strings.HasPrefix(pathLit, "connectors/") {
					t.Errorf("%s imports connector module %s", filepath.Base(path), pathLit)
				}
				if imp.Name != nil && imp.Name.Name == "_" && strings.Contains(pathLit, "connectors") {
					t.Errorf("%s blank-imports connector %s", filepath.Base(path), pathLit)
				}
			}
		}
	}
}

func TestNoFixedOptional_StandardBackendBundleIsEssentialOnly(t *testing.T) {
	t.Parallel()
	keys := standardplugins.UpstreamAPIKeys{}
	got := idsOf(standardplugins.StandardBackendBundle(keys))
	want := append([]string(nil), standardplugins.EssentialBackendKinds...)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("StandardBackendBundle=%v want essential-only %v", got, want)
	}
	for _, id := range got {
		if !standardplugins.IsEssentialBackendKind(id) {
			t.Fatalf("non-essential kind %q remains in StandardBackendBundle", id)
		}
	}
}

func TestEssentialOnly_AllowlistExact(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	ids := reg.BackendFactoryIDs()
	want := append([]string(nil), standardplugins.EssentialBackendKinds...)
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("InstallStandardBackendsOn=%v want essential-only %v", ids, want)
	}
}

func TestDynamic_NoOptionalKindNamesInGenericPluginreg(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "pluginreg")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenLits := []string{
		`"openrouter"`, `"opencode"`, `"opencode-go"`, `"opencode-zen"`,
		`"openai-codex"`, `"openai-codex-app-server"`, `"ollama"`, `"cursorcliacp"`,
		`"huggingface"`, `"llamacpp"`, `"lmstudio"`, `"nvidia"`, `"vllm"`,
		`"geminicliacp"`, `"agycliacp"`, `"acp"`,
	}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, bad := range forbiddenLits {
				if lit.Value == bad {
					t.Errorf("pluginreg/%s contains optional kind literal %s", ent.Name(), bad)
				}
			}
			return true
		})
	}
}

func TestDynamic_OptionalConnectorManifestsDoNotRequireRootGoEdit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	connectorsRoot := filepath.Join(root, "connectors")
	ents, err := os.ReadDir(connectorsRoot)
	if err != nil {
		t.Fatal(err)
	}
	essential := map[string]struct{}{}
	for _, k := range standardplugins.EssentialBackendKinds {
		essential[k] = struct{}{}
	}
	bundleIDs := map[string]struct{}{}
	for _, id := range idsOf(standardplugins.StandardBackendBundle(standardplugins.UpstreamAPIKeys{})) {
		bundleIDs[id] = struct{}{}
	}
	var optionalKinds []string
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		manifestPath := filepath.Join(connectorsRoot, ent.Name(), "manifest", "template.backendplugin.json")
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var m struct {
			Exports []struct {
				Kind string `json:"kind"`
			} `json:"exports"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", manifestPath, err)
		}
		for _, exp := range m.Exports {
			kind := strings.TrimSpace(exp.Kind)
			if kind == "" {
				continue
			}
			if _, ok := essential[kind]; ok {
				t.Fatalf("connector manifest %s exports essential kind %q", manifestPath, kind)
			}
			if _, ok := bundleIDs[kind]; ok {
				t.Fatalf("optional kind %q from %s is still statically registered in StandardBackendBundle", kind, manifestPath)
			}
			optionalKinds = append(optionalKinds, kind)
		}
	}
	if len(optionalKinds) == 0 {
		t.Fatal("expected at least one optional connector manifest export under connectors/*/manifest")
	}
	// Wire-model defaults must not keep special cases for migrated optional kinds.
	// Identity YAML keys (e.g. openrouter attribution) are wire-compat and out of scope.
	wireModel := filepath.Join(root, "internal", "standardplugins", "default_wire_model.go")
	raw, err := os.ReadFile(wireModel)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, kind := range optionalKinds {
		needle := `case "` + kind + `"`
		if strings.Contains(src, needle) {
			t.Errorf("default_wire_model.go still has optional kind switch %s; optional kinds must be manifest-only", needle)
		}
	}
}

func idsOf(b standardplugins.Bundle) []string {
	out := make([]string, 0, len(b.Backends))
	for _, e := range b.Backends {
		out = append(out, e.ID)
	}
	return out
}

func readProductionGoSources(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}
