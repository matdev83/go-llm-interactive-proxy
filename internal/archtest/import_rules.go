package archtest

import (
	"fmt"
	"sort"
	"strings"
)

// ForbiddenImportRule forbids an import edge from source packages to a target pattern.
type ForbiddenImportRule struct {
	SourcePattern string // repo-relative package dir pattern
	TargetPattern string // import path (exact package) or substring marker starting with /
	Reason        string
	ExceptPrefix  []string // allowed import path prefixes
}

// ForbiddenImports is the permanent package-level import deny-list.
var ForbiddenImports = []ForbiddenImportRule{
	{
		SourcePattern: "internal/stdhttp/contract",
		TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle",
		Reason:        "contract package must stay cycle-neutral",
	},
	{
		SourcePattern: "internal/stdhttp/contract",
		TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp",
		Reason:        "contract must not import root stdhttp",
	},
	{
		SourcePattern: "internal/infra/runtimebundle",
		TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp",
		Reason:        "runtimebundle must import stdhttp/contract, not root stdhttp",
		ExceptPrefix: []string{
			"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/",
		},
	},
	{
		SourcePattern: "pkg/lipruntime",
		TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload",
		Reason:        "public facade uses pkg/lipsdk/configreload",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/infra/runtimebundle",
		Reason:        "core must not import composition runtimebundle",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/infra/runtimehost",
		Reason:        "core must not import composition runtimehost",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/infra/configsource",
		Reason:        "core must not import configsource watcher adapter",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/stdhttp",
		Reason:        "core must not import stdhttp driving adapters",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "os/signal",
		Reason:        "core must not import signal driving adapters",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "*fsnotify*",
		Reason:        "core must not import filesystem watchers",
	},
	{
		SourcePattern: "internal/core",
		TargetPattern: "github.com/rjeczalik/notify",
		Reason:        "core must not import filesystem watchers",
	},
}

// fileScopedImportRule restricts specific production files.
type fileScopedImportRule struct {
	File          string
	TargetPattern string
	Reason        string
}

var fileScopedImportRules = []fileScopedImportRule{
	{File: "internal/infra/runtimebundle/inspect.go", TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost", Reason: "inspect purity"},
	{File: "internal/infra/runtimebundle/inspect.go", TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp", Reason: "inspect purity"},
	{File: "internal/infra/runtimebundle/inspect.go", TargetPattern: "log/slog", Reason: "inspect purity"},
	{File: "internal/infra/runtimebundle/validate_distribution.go", TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost", Reason: "validation purity"},
	{File: "internal/infra/runtimebundle/validate_distribution.go", TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp", Reason: "validation purity"},
	{File: "internal/stdhttp/generation_host.go", TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle", Reason: "serve adapter drops ownership imports"},
	{File: "internal/stdhttp/generation_host.go", TargetPattern: "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost", Reason: "serve adapter drops ownership imports"},
}

// ScanForbiddenImports reports production import edges matching ForbiddenImports
// and file-scoped purity rules.
func ScanForbiddenImports(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		imports := FileImportPaths(f)
		for _, rule := range ForbiddenImports {
			if !MatchPathPrefix(pkg, rule.SourcePattern) {
				continue
			}
			for _, imp := range imports {
				if importExemptPrefix(imp, rule.ExceptPrefix) {
					continue
				}
				if matchImportTarget(imp, rule.TargetPattern) {
					out = append(out, RuleFinding{
						Rule:   "forbidden_import",
						Path:   rel,
						Detail: "import " + imp + " (" + rule.Reason + ")",
					})
				}
			}
		}
		for _, rule := range fileScopedImportRules {
			if rel != rule.File {
				continue
			}
			for _, imp := range imports {
				if matchImportTarget(imp, rule.TargetPattern) {
					// Root stdhttp forbid should not match stdhttp/contract subpath.
					if rule.TargetPattern == "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp" &&
						strings.HasPrefix(imp, "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/") {
						continue
					}
					out = append(out, RuleFinding{
						Rule:   "forbidden_import",
						Path:   rel,
						Detail: "import " + imp + " (" + rule.Reason + ")",
					})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func importExemptPrefix(imp string, except []string) bool {
	for _, e := range except {
		if imp == e || strings.HasPrefix(imp, e) {
			return true
		}
	}
	return false
}

func matchImportTarget(imp, pattern string) bool {
	switch {
	case pattern == "":
		return false
	case strings.HasPrefix(pattern, "/") && !strings.HasSuffix(pattern, "/") && strings.Contains(pattern, "/internal/"):
		return strings.Contains(imp, pattern)
	case strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*"):
		return strings.Contains(imp, strings.Trim(pattern, "*"))
	case imp == pattern:
		return true
	case strings.HasPrefix(imp, pattern+"/"):
		// Exact package forbid does not include subpackages.
		return false
	default:
		return MatchImportPattern(imp, pattern)
	}
}
