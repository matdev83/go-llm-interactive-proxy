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
		SourcePattern: "internal/core/runtime",
		TargetPattern: "/internal/core/tokenaccounting/ledger",
		Reason:        "runtime must not write the legacy token ledger; sealed TUR/LUR owns financial evidence",
	},
	{
		SourcePattern: "internal/core/runtime",
		TargetPattern: "/internal/infra/billingstore",
		Reason:        "runtime must not import journal settlement; post-turn billing owns monetary truth",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "github.com/openai/openai-go",
		Reason:        "billing must not import provider SDKs",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "github.com/anthropics/anthropic-sdk-go",
		Reason:        "billing must not import provider SDKs",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "google.golang.org/genai",
		Reason:        "billing must not import provider SDKs",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "github.com/aws/aws-sdk-go-v2",
		Reason:        "billing must not import provider SDKs",
	},
	{
		SourcePattern: "internal/core/billing",
		TargetPattern: "/pkg/lipapi",
		Reason:        "billing evidence stays provider-neutral and lipapi-free",
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
	{
		SourcePattern: "internal/core",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "core must not import protocol wire codecs",
	},
	{
		SourcePattern: "internal/plugins/frontends",
		TargetPattern: "/internal/refclient",
		Reason:        "production frontend adapters must not import reference clients",
	},
	{
		SourcePattern: "internal/plugins/backends",
		TargetPattern: "/internal/refbackend",
		Reason:        "production backend adapters must not import reference backends",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "/internal/refclient",
		Reason:        "production wire codec must remain independent of reference clients",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "/internal/refbackend",
		Reason:        "production wire codec must remain independent of reference backends",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "github.com/openai/openai-go",
		Reason:        "openresponses must not import provider SDKs",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "github.com/anthropics/anthropic-sdk-go",
		Reason:        "openresponses must not import provider SDKs",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "github.com/google/generative-ai-go",
		Reason:        "openresponses must not import provider SDKs",
	},
	{
		SourcePattern: "internal/plugins/protocols/openresponses",
		TargetPattern: "github.com/aws/aws-sdk-go-v2",
		Reason:        "openresponses must not import provider SDKs",
	},
	{
		SourcePattern: "pkg/lipapi",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "public canonical SDK must not import protocol wire codecs",
	},
	{
		SourcePattern: "pkg/lipsdk",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "public plugin SDK must not import protocol wire codecs",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "reference client emulator must not import production OpenResponses codecs",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/plugins/frontends/openresponses",
		Reason:        "reference client emulator must not import production OpenResponses frontend",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/plugins/backends/openresponsescompat",
		Reason:        "reference client emulator must not import production OpenResponses backend",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/refbackend",
		Reason:        "reference client emulator must not import reference backend emulators",
	},
	{
		SourcePattern: "internal/refclient/openresponses",
		TargetPattern: "/internal/testkit/conformance",
		Reason:        "reference client emulator must not import the conformance matrix",
	},
	{
		SourcePattern: "internal/refbackend/openresponses",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "reference backend emulator must not import production OpenResponses codecs",
	},
	{
		SourcePattern: "internal/refbackend/openresponses",
		TargetPattern: "/internal/plugins/frontends/openresponses",
		Reason:        "reference backend emulator must not import production OpenResponses frontend",
	},
	{
		SourcePattern: "internal/refbackend/openresponses",
		TargetPattern: "/internal/plugins/backends/openresponsescompat",
		Reason:        "reference backend emulator must not import production OpenResponses backend",
	},
	{
		SourcePattern: "internal/refbackend/openresponses",
		TargetPattern: "/internal/refclient",
		Reason:        "reference backend emulator wire code must not import reference client wire types",
	},
	{
		SourcePattern: "internal/refbackend/openresponses",
		TargetPattern: "/internal/testkit/conformance",
		Reason:        "reference backend emulator must not import the conformance matrix",
	},
	{
		SourcePattern: "internal/refbackend/openresponses",
		TargetPattern: "/internal/testkit/openresponses",
		Reason:        "reference backend emulator must not import testkit wire contracts",
	},
	{
		SourcePattern: "internal/plugins/frontends/openresponses",
		TargetPattern: "/internal/refbackend",
		Reason:        "production frontend must not import reference backend emulators",
	},
	{
		SourcePattern: "internal/plugins/backends/openresponsescompat",
		TargetPattern: "/internal/refbackend",
		Reason:        "production backend must not import reference backend emulators",
	},
	{
		SourcePattern: "internal/testkit/openresponses",
		TargetPattern: "/internal/plugins/protocols/openresponses",
		Reason:        "testkit contracts must not import production OpenResponses codecs",
	},
	{
		SourcePattern: "internal/plugins/backends/openresponsescompat",
		TargetPattern: "github.com/openai/openai-go",
		Reason:        "generic OpenResponses backend must not import provider SDKs",
	},
	{
		SourcePattern: "internal/plugins/backends/openresponsescompat",
		TargetPattern: "/connectors/",
		Reason:        "generic OpenResponses backend must not import provider connectors",
	},
	{
		SourcePattern: "internal/plugins/backends/openresponsescompat",
		TargetPattern: "/connector-support/",
		Reason:        "generic OpenResponses backend must not import provider connector support",
	},
	{
		SourcePattern: "internal/plugins/frontends",
		TargetPattern: "/internal/core/routeoverride",
		Reason:        "frontend plugins must not import route-override state",
	},
	{
		SourcePattern: "internal/plugins/backends",
		TargetPattern: "/internal/core/routeoverride",
		Reason:        "backend plugins must not import route-override state",
	},
	{
		SourcePattern: "pkg/lipapi",
		TargetPattern: "/internal/core/routeoverride",
		Reason:        "canonical contracts must not import route-override state",
	},
	{
		SourcePattern: "pkg/lipsdk",
		TargetPattern: "/internal/core/routeoverride",
		Reason:        "public SDK must not import route-override state",
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
