package selfdefense_test

import (
	"net/http"
	"sort"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/configreload"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/selfdefense"
	"gopkg.in/yaml.v3"
)

func claimsConfigNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return node
}

// standardFrontendClaimConfigs are the configuration shapes the standard
// distribution can present to a frontend route-claims provider. The inventory is
// the real contribution map; only the provider input varies.
var standardFrontendClaimConfigs = []string{
	"{}",
	"websocket:\n  enabled: true\n",
	"websocket:\n  enabled: false\n",
	"base_path: /v1\n",
	"base_path: /openresponses/v1\n",
	"base_path: /openresponses/v1\nwebsocket:\n  enabled: true\n",
	"base_path: /api/v1\n",
}

func standardFrontendRouteClaims(t *testing.T) []httpcontract.RouteClaim {
	t.Helper()

	providers := standardplugins.StandardFrontendRouteClaims()
	if len(providers) == 0 {
		t.Fatal("the standard distribution must declare frontend route-claims providers")
	}
	owners := make([]string, 0, len(providers))
	for owner := range providers {
		owners = append(owners, owner)
	}
	sort.Strings(owners)

	seen := map[string]bool{}
	var claims []httpcontract.RouteClaim
	for _, owner := range owners {
		provider := providers[owner]
		for i, raw := range standardFrontendClaimConfigs {
			got, err := provider(owner, claimsConfigNode(t, raw))
			if err != nil {
				t.Fatalf("owner %q config %d: route claims: %v", owner, i, err)
			}
			if len(got) == 0 {
				t.Fatalf("owner %q config %d: declared route claims but produced none", owner, i)
			}
			for _, claim := range got {
				if _, err := claim.NormalizedClaim(); err != nil {
					t.Fatalf("owner %q config %d: claim %+v: %v", owner, i, claim, err)
				}
				key := claim.OwnerID + " " + claim.Method + " " + claim.Path
				if seen[key] {
					continue
				}
				seen[key] = true
				claims = append(claims, claim)
			}
		}
	}
	return claims
}

// TestBuiltInImpossiblePathSetDoesNotOverlapStandardFrontendRoutes is the
// requirement 3.6 non-collision regression. The inventory is the real standard
// distribution route-claims seam, not a hand-written list, so a future frontend
// that claims one of the built-in impossible paths fails here.
func TestBuiltInImpossiblePathSetDoesNotOverlapStandardFrontendRoutes(t *testing.T) {
	t.Parallel()

	claims := standardFrontendRouteClaims(t)
	if len(claims) < 8 {
		t.Fatalf("standard frontend claims = %d, want the full standard surface", len(claims))
	}
	for _, claim := range claims {
		if selfdefense.ImpossiblePath(claim.Path) {
			t.Errorf("built-in impossible-path set collides with standard route %s %s (owner %q, kind %q)",
				claim.Method, claim.Path, claim.OwnerID, claim.Kind)
		}
		if http.MethodGet == claim.Method && selfdefense.ImpossiblePath(claim.Path+"/") {
			t.Errorf("built-in impossible-path set collides with the trailing-slash form of %s %s", claim.Method, claim.Path)
		}
	}
}

// TestBuiltInImpossiblePathSetDoesNotOverlapManagementRecoverySurface proves the
// separate management/recovery listener keeps working management paths: the
// deterministic matcher must never claim one of them.
func TestBuiltInImpossiblePathSetDoesNotOverlapManagementRecoverySurface(t *testing.T) {
	t.Parallel()

	management := []string{configreload.ReloadPath, configreload.StatusPath}
	if management[0] == "" || management[1] == "" {
		t.Fatal("the management/recovery surface must publish its fixed paths")
	}
	for _, path := range management {
		if selfdefense.ImpossiblePath(path) {
			t.Errorf("built-in impossible-path set collides with management path %q", path)
		}
	}
}
