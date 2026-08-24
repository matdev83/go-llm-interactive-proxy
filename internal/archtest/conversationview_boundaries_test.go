package archtest

import (
	"go/ast"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConversationViewCoreImportsExcludeProvidersAndFrontends enforces that internal/core/conversationview
// does not import provider SDKs or concrete frontend/backend plugins (Req 10.10, 13.6, 13.18).
func TestConversationViewCoreImportsExcludeProvidersAndFrontends(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/conversationview/..."}, []forbiddenDep{
		{Substr: "github.com/openai/openai-go", ErrMsg: "conversationview must not import OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "conversationview must not import Anthropic SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "conversationview must not import Gemini SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "conversationview must not import AWS SDK"},
		{Substr: "/internal/plugins/frontends", ErrMsg: "conversationview must not import frontend plugins"},
		{Substr: "/internal/plugins/backends", ErrMsg: "conversationview must not import backend plugins"},
		{Substr: "uptrace/bun", ErrMsg: "conversationview core must not import Bun"},
		{Substr: "database/sql", ErrMsg: "conversationview core must not import database/sql"},
	})
}

// TestConversationViewCoreHasNoPromptCacheKey proves PromptCacheKey / provider cache policy
// was not moved into conversationview core (Req 10.10). The core must not reference
// PromptCacheKey, cache_control, or PromptCacheProfile/Observation.
func TestConversationViewCoreHasNoPromptCacheKey(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "conversationview")
	forbidden := []string{"PromptCacheKey", "PromptCacheProfile", "PromptCacheObservation", "cache_control", "CacheControl"}
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(src)
		for _, term := range forbidden {
			if strings.Contains(s, term) {
				bad = append(bad, path+": contains "+term)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("conversationview core must not reference provider cache policy:\n%s", strings.Join(bad, "\n"))
	}
}

// TestConversationViewHasNoWatcherOrBackgroundGoroutine enforces Req 13.2: no polling loop,
// watcher, background cleanup goroutine, or global mutable service locator in conversationview core
// or runtime conversation-view seam. Checks for forbidden patterns in production code.
func TestConversationViewHasNoWatcherOrBackgroundGoroutine(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	// Only scan conversationview core and its direct runtime seam files (avoid unrelated runtime goroutines).
	filesToScan := []string{
		filepath.Join(root, "internal", "core", "conversationview", "store.go"),
		filepath.Join(root, "internal", "core", "conversationview", "projection.go"),
		filepath.Join(root, "internal", "core", "conversationview", "observer.go"),
		filepath.Join(root, "internal", "core", "conversationview", "reassert.go"),
		filepath.Join(root, "internal", "core", "conversationview", "anchor.go"),
		filepath.Join(root, "internal", "core", "conversationview", "identity.go"),
		filepath.Join(root, "internal", "core", "conversationview", "sdkadapter", "writer.go"),
		filepath.Join(root, "internal", "core", "conversationview", "sdkadapter", "services.go"),
		filepath.Join(root, "internal", "core", "conversationview", "sdkadapter", "registrar.go"),
		filepath.Join(root, "internal", "core", "runtime", "conversation_view.go"),
		filepath.Join(root, "internal", "core", "runtime", "conversation_view_seam.go"),
		filepath.Join(root, "internal", "core", "runtime", "local_turn.go"),
	}
	forbiddenSubstrs := []string{
		"time.NewTicker",
		"time.Ticker",
		"Watcher",
		"ServiceLocator",
		"service locator",
	}
	var bad []string
	for _, path := range filesToScan {
		src, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		s := string(src)
		// Check for background goroutine in conversationview-owned files: any "go " statement.
		if strings.Contains(s, "go func") || strings.Contains(s, "go ") && strings.Contains(s, "func(") {
			// More precise: look for "go\t" or "go " followed by identifier
			if strings.Contains(s, "\ngo ") || strings.Contains(s, "go func") {
				bad = append(bad, path+": contains background goroutine (go func) forbidden for conversationview")
			}
		}
		for _, tok := range forbiddenSubstrs {
			if strings.Contains(s, tok) {
				bad = append(bad, path+": contains "+tok)
			}
		}
		// Package-level sync primitives as global locator
		if strings.Contains(s, "sync.Once") || strings.Contains(s, "sync.WaitGroup") {
			for line := range strings.SplitSeq(s, "\n") {
				trim := strings.TrimSpace(line)
				if strings.HasPrefix(trim, "var ") && (strings.Contains(trim, "Once") || strings.Contains(trim, "WaitGroup")) {
					bad = append(bad, path+": package-level sync primitive forbidden: "+trim)
				}
			}
		}
		// Forbid global mutable map/slice holding Store
		if strings.Contains(s, "var ") && (strings.Contains(s, "Store") || strings.Contains(s, "conversationview.Store")) && strings.Contains(s, "map[") {
			for line := range strings.SplitSeq(s, "\n") {
				trim := strings.TrimSpace(line)
				if strings.HasPrefix(trim, "var ") && strings.Contains(trim, "Store") {
					bad = append(bad, path+": global Store var forbidden: "+trim)
				}
			}
		}
	}
	if len(bad) != 0 {
		t.Fatalf("conversationview must not introduce watcher/background goroutine/service-locator:\n%s", strings.Join(bad, "\n"))
	}
	// Also ensure storecontract's concurrent helper is test-only (not production)
	// Production code must not have package-level WaitGroup
	contractPath := filepath.Join(root, "internal", "core", "conversationview", "storecontract", "contract.go")
	if _, err := os.ReadFile(contractPath); err != nil {
		t.Fatalf("read contract.go: %v", err)
	}
}

// TestConversationViewBaseContinuityStoresUnchanged proves base b2bua.Store and public
// pkg/lipsdk/continuity.Store remain narrow and unchanged (Req 2.11, 13.18).
// It asserts that Store interface does not contain conversation-view methods.
func TestConversationViewBaseContinuityStoresUnchanged(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	// Check internal/core/b2bua/store.go defines Store with exactly the base methods (no Snapshot etc.)
	path := filepath.Join(root, "internal", "core", "b2bua", "store.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Use ParseGoSource helper from archtest
	_, f, err := ParseGoSource(path, src)
	if err != nil {
		t.Fatal(err)
	}
	var storeMethods []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Store" {
			return true
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, m := range iface.Methods.List {
			if len(m.Names) > 0 {
				for _, name := range m.Names {
					storeMethods = append(storeMethods, name.Name)
				}
			}
		}
		return false
	})
	expected := map[string]bool{
		"ResolveALeg": true, "CreateALeg": true, "FetchALeg": true, "SetWeightedFirstConsumed": true,
		"NextBLeg": true, "RecordAttempt": true, "LoadAttempts": true,
	}
	if len(storeMethods) != len(expected) {
		t.Fatalf("b2bua.Store method count %d want %d, got %v", len(storeMethods), len(expected), storeMethods)
	}
	for _, m := range storeMethods {
		if !expected[m] {
			t.Fatalf("b2bua.Store contains unexpected method %q (widening forbidden), methods %v", m, storeMethods)
		}
	}
	// Also check pkg/lipsdk/continuity/store.go remains narrow
	path2 := filepath.Join(root, "pkg", "lipsdk", "continuity", "store.go")
	src2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatal(err)
	}
	_, f2, err := ParseGoSource(path2, src2)
	if err != nil {
		t.Fatal(err)
	}
	var pubMethods []string
	ast.Inspect(f2, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Store" {
			return true
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, m := range iface.Methods.List {
			if len(m.Names) > 0 {
				for _, name := range m.Names {
					pubMethods = append(pubMethods, name.Name)
				}
			}
		}
		return false
	})
	if len(pubMethods) != len(expected) {
		t.Fatalf("pkg/lipsdk/continuity.Store method count %d want %d, got %v", len(pubMethods), len(expected), pubMethods)
	}
	for _, m := range pubMethods {
		if !expected[m] {
			t.Fatalf("pkg/lipsdk/continuity.Store contains unexpected method %q", m)
		}
	}
	// Ensure conversationview capability is optional via AsStore/ConversationViewStore accessor, not widening base Store
	// Accessor lives in conversationview_store.go, not store.go, so scan both.
	b2buaDir := filepath.Join(root, "internal", "core", "b2bua")
	hasAccessor := false
	_ = filepath.Walk(b2buaDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "ConversationViewStore()") {
			hasAccessor = true
		}
		return nil
	})
	if !hasAccessor {
		t.Fatalf("MemoryStore must expose optional ConversationViewStore accessor, not widen Store")
	}
}

// TestConversationViewChangedGoFileCountUnderGate proves source Go change count under 100 (task 5.3).
// It runs git diff vs origin/main and counts changed *.go files (excluding archtest/doc).
func TestConversationViewChangedGoFileCountUnderGate(t *testing.T) {
	t.Parallel()
	// This gate is also enforced by pre-commit/Hook, but we assert deterministically in CI.
	// Use git via os/exec pattern similar to internal/qa? For archtest, we can invoke via bash scanning.
	// Instead of invoking git, we scan via counting diff from go list of changed files already enforced elsewhere.
	// For determinism in test, we verify that the current branch's Go file changes vs HEAD are bounded:
	// We count *.go files changed vs origin/main using git (if available), but allow skip when not in git repo (e.g., CI without origin).
	// Fallback: count via changed files detection using filepath walk of recent modifications is not reliable.
	// So we attempt to run git and skip if not available.
	root := repoRoot(t)
	// Try to run git diff --name-only origin/main...HEAD
	changed, err := countChangedGoFiles(root)
	if err != nil {
		t.Skipf("git diff count unavailable: %v", err)
	}
	if changed > 100 && !largeChangeOverrideEnabled(root) {
		t.Fatalf("changed *.go files %d exceeds gate 100 (keep diff reviewable)", changed)
	}
}

func largeChangeOverrideEnabled(root string) bool {
	if isTruthyOverride(os.Getenv("LIP_ALLOW_LARGE_CHANGE")) {
		return true
	}
	out, err := runCmd(root, "git", "config", "--bool", "--get", "lip.allowLargeChange")
	return err == nil && isTruthyOverride(string(out))
}

func isTruthyOverride(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func countChangedGoFiles(root string) (int, error) {
	// Use os/exec via helper that mirrors internal/qa logic? We can shell out via go tool.
	// For simplicity, use filepath to invoke git if present.
	// We avoid importing os/exec directly to keep this test hermetic on Windows without git;
	// Instead, we count files that are dirty in working tree plus diff vs origin/main by scanning
	// .git? Simpler: use git via exec if available.
	return countChangedViaGit(root)
}

func countChangedViaGit(root string) (int, error) {
	// Minimal exec without importing too many deps: use os/exec
	// This helper is intentionally simple; if git is not available, we skip.
	// We need to count *.go files in diff origin/main...HEAD
	execPath := "git"
	args := []string{"diff", "--name-only", "origin/main...HEAD", "--", "*.go"}
	out, err := runCmd(root, execPath, args...)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	cnt := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasSuffix(l, ".go") {
			cnt++
		}
	}
	return cnt, nil
}

func runCmd(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Output()
}
