package archtest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoreIngressDefenseIsAdmittedKernelPackage locks the kernel admission of
// internal/core/ingressdefense. Default ingress self-defense is provider- and
// protocol-neutral security that the standard distribution still requires when
// optional feature plugins are absent, so it is a core kernel invariant rather
// than an infrastructure-owned policy or a feature plugin.
func TestCoreIngressDefenseIsAdmittedKernelPackage(t *testing.T) {
	t.Parallel()

	entry, ok := coreOwnershipByPackage()["ingressdefense"]
	if !ok {
		t.Fatal("internal/core/ingressdefense holds production code but has no core-ownership manifest entry; " +
			"add the explicit kernel-invariant rationale before the package may stay in core")
	}
	if entry.Category != CoreOwnershipKernelInvariant {
		t.Fatalf("ingressdefense category = %q, want %q", entry.Category, CoreOwnershipKernelInvariant)
	}
	reason := strings.ToLower(entry.Reason)
	if !strings.Contains(reason, "default") || !strings.Contains(reason, "optional") {
		t.Fatalf("ingressdefense rationale must record why default ingress security stays required when optional features are absent, got %q", entry.Reason)
	}
}

// TestCoreIngressDefensePackageIsDocumentedAndNeutral pins the package boundary:
// the kernel package must exist with a reviewable package doc, and it must not
// reach for HTTP, context, clock-free I/O, storage, public SDK contracts, the
// config package that compiles it, or the HTTP/auth driving adapters.
func TestCoreIngressDefensePackageIsDocumentedAndNeutral(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "ingressdefense")
	if !dirHasProductionGo(dir) {
		t.Fatal("internal/core/ingressdefense must exist and hold production Go policy code")
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.go")); err != nil {
		t.Fatalf("internal/core/ingressdefense must document its boundary in doc.go: %v", err)
	}

	out, err := cachedGoList(t, "-json", "-test=false", "./internal/core/ingressdefense")
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if !dec.More() {
		t.Fatal("go list: empty output")
	}
	var pkg goListPackage
	if err := dec.Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	const wantPath = "github.com/matdev83/go-llm-interactive-proxy/internal/core/ingressdefense"
	if pkg.ImportPath != wantPath {
		t.Fatalf("unexpected package: got %q want %q", pkg.ImportPath, wantPath)
	}
	forbidden := []forbiddenDep{
		{Substr: "net/http", ErrMsg: "core ingress defense must not depend on net/http; keep HTTP at stdhttp driving adapters"},
		{Substr: "net/url", ErrMsg: "core ingress defense must not depend on request-target parsing"},
		{Substr: "context", ErrMsg: "core ingress defense owns no cancellable I/O boundary"},
		{Substr: "os", ErrMsg: "core ingress defense owns no process or filesystem state"},
		{Substr: "database/sql", ErrMsg: "core ingress defense must not depend on database/sql; adaptive state is process-local"},
		{Substr: "uptrace/bun", ErrMsg: "core ingress defense must not depend on Bun; adaptive state is process-local"},
		{Substr: "github.com/matdev83/go-llm-interactive-proxy/pkg/", ErrMsg: "core ingress defense must not add a public contract (requirements 10.1/10.2)"},
		{Substr: "github.com/matdev83/go-llm-interactive-proxy/internal/core/config", ErrMsg: "core ingress defense must not depend on the config package that compiles it"},
		{Substr: "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp", ErrMsg: "core ingress defense must not depend on the stdhttp driving adapter"},
		{Substr: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins", ErrMsg: "core ingress defense must not depend on concrete plugins"},
		{Substr: "github.com/matdev83/go-llm-interactive-proxy/internal/infra", ErrMsg: "core ingress defense must not depend on infrastructure composition"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "core ingress defense must not depend on a provider SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "core ingress defense must not depend on a provider SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "core ingress defense must not depend on a provider SDK"},
	}
	for _, imp := range pkg.Imports {
		for _, r := range forbidden {
			if strings.Contains(imp, r.Substr) {
				t.Fatalf("%s: %s", r.ErrMsg, imp)
			}
		}
	}
}

// TestCoreIngressDefenseCarriesNoAggregateNetworkState proves the exact-IP-only
// source-defense contract cannot regress into subnet, ASN, or country state.
// The only prefix surface is the read-only adaptive exemption allowlist on the
// policy value; no source-state type may be keyed or widened by prefix.
func TestCoreIngressDefenseCarriesNoAggregateNetworkState(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "ingressdefense")
	forbidden := []string{"netip.PrefixFrom", "ASN", "AutonomousSystem", "Country"}
	var found []string
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
		for _, term := range forbidden {
			if strings.Contains(string(src), term) {
				found = append(found, filepath.Base(path)+" ("+term+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("ingress defense must keep exact-address-only state and no ASN/country escalation:\n%s", strings.Join(found, "\n"))
	}
}
