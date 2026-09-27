package selfdefense_test

import (
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
//
// base_path is an arbitrary operator-configurable normalized absolute non-root
// path, not one of a fixed handful, so this list deliberately includes base paths
// that fall INSIDE the frozen impossible-path families. Those are the cases that
// used to be untested: they are valid configurations that publish real routes the
// deterministic matcher would otherwise answer with a generic 404. They are
// asserted against the owned-route carve rather than against the bare families.
var standardFrontendClaimConfigs = []string{
	"{}",
	"websocket:\n  enabled: true\n",
	"websocket:\n  enabled: false\n",
	"base_path: /v1\n",
	"base_path: /openresponses/v1\n",
	"base_path: /openresponses/v1\nwebsocket:\n  enabled: true\n",
	"base_path: /api/v1\n",
	// Operator-chosen base paths inside frozen impossible-path families.
	"base_path: /wp-admin\n",
	"base_path: /wp-content\n",
	"base_path: /cgi-bin\n",
	"base_path: /phpmyadmin\n",
	"base_path: /pma\n",
	"base_path: /adminer\n",
	"base_path: /vendor/phpunit\n",
	"base_path: /.github\n",
	"base_path: /.gitlab\n",
	"base_path: /.ht\n",
	"base_path: /.git\n",
	"base_path: /.env\n",
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

// TestBuiltInImpossiblePathSetNeverMakesAPublishedRouteUnreachable is the
// requirement 3.6 non-collision regression. The inventory is the real standard
// distribution route-claims seam, not a hand-written list, so a future frontend
// that claims a colliding path fails here.
//
// The invariant is deliberately NOT "the frozen families never overlap a standard
// route". base_path is an arbitrary operator-configurable normalized absolute
// non-root path, so overlap with a frozen family is a legitimate, accepted
// configuration rather than a bug. The invariant that must hold is the one an
// operator depends on: a published route is never refused, for ANY base path. Each
// claim is therefore checked against the owned-route carve the generation actually
// supplies, which is what makes this exhaustive over the base_path space instead of
// dependent on a sampled list of configurations.
func TestBuiltInImpossiblePathSetNeverMakesAPublishedRouteUnreachable(t *testing.T) {
	t.Parallel()

	claims := standardFrontendRouteClaims(t)
	if len(claims) < 8 {
		t.Fatalf("standard frontend claims = %d, want the full standard surface", len(claims))
	}
	colliding := 0
	for _, claim := range claims {
		for _, path := range []string{claim.Path, claim.Path + "/"} {
			// The generation publishes this route, so the gate is given this route
			// as an owned root.
			if selfdefense.ImpossiblePath(path, path) {
				t.Errorf("published route %s %s (owner %q, kind %q) is still refused as impossible for a self-owned route",
					claim.Method, path, claim.OwnerID, claim.Kind)
			}
		}
		if selfdefense.ImpossiblePath(claim.Path) {
			colliding++
		}
	}
	// The fixture must actually exercise the overlapping case, or this test would
	// pass vacuously on a distribution that never collides.
	if colliding == 0 {
		t.Fatal("no standard frontend claim collides with a frozen family: the colliding base paths in the fixture are no longer being exercised")
	}
	// That a published root does not widen the carve beyond the published routes
	// is asserted precisely, with hand-picked colliding roots, in owned_routes_test.go.
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
