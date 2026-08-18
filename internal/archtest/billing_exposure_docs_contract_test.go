package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 7.3: durable project memory must teach exposure admission, not hold/TUR handoff.
func TestBillingExposureDocsRejectRetiredHoldAuthorizeInvariant(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	forbid := []struct {
		rel     string
		needles []string
	}{
		{
			rel: "AGENTS.md",
			needles: []string{
				"pessimistic authorize",
				"sealed TUR/LUR handoff",
				"TUR/LUR, holds",
			},
		},
		{
			rel: ".kiro/AGENTS.md",
			needles: []string{
				"authorize, then TUR/LUR handoff",
			},
		},
		{
			rel: ".kiro/steering/product.md",
			needles: []string{
				"Authorize a pessimistic hold",
				"seal TUR/LUR at the terminal owner",
			},
		},
		{
			rel: ".kiro/steering/structure.md",
			needles: []string{
				"owns authorization, immutable TUR/LUR",
				"Change TUR/LUR / journal / holds",
			},
		},
		{
			rel: "docs/billing-host-composition.md",
			needles: []string{
				"is retained only long enough",
			},
		},
	}

	require := []struct {
		rel     string
		needles []string
	}{
		{
			rel: "AGENTS.md",
			needles: []string{
				"cheap credit screen",
				"operational exposure",
				"BillingCallID",
			},
		},
		{
			rel: ".kiro/AGENTS.md",
			needles: []string{
				"cheap credit screen",
				"operational exposure",
			},
		},
		{
			rel: ".kiro/steering/product.md",
			needles: []string{
				"cheap credit screen",
				"atomic operational exposure",
				"BillingCallID",
			},
		},
		{
			rel: ".kiro/steering/structure.md",
			needles: []string{
				"BillingCallID",
				"operational exposure",
				"post-usage",
			},
		},
		{
			rel: "docs/billing-host-composition.md",
			needles: []string{
				"settled-credit screen",
				"atomic exposure admission",
				"local durable terminal spool",
				"complete-call gate",
				"(recorded_at, transaction_id)",
				"all-or-none",
			},
		},
	}

	for _, row := range forbid {
		text := readRepoFile(t, root, row.rel)
		for _, needle := range row.needles {
			if strings.Contains(text, needle) {
				t.Fatalf("%s still teaches retired hold/TUR invariant with %q", row.rel, needle)
			}
		}
	}
	for _, row := range require {
		text := readRepoFile(t, root, row.rel)
		for _, needle := range row.needles {
			if !strings.Contains(text, needle) {
				t.Fatalf("%s missing required exposure-flow marker %q", row.rel, needle)
			}
		}
	}
}

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}
