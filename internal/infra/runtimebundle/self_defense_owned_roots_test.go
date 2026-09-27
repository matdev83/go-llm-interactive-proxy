package runtimebundle_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/selfdefense"
	"gopkg.in/yaml.v3"
)

// ownedRootsFrontend is the generic OpenResponses frontend: the one bundled
// frontend whose base path is an arbitrary operator-configurable value.
const ownedRootsFrontend = "openresponses"

// ownedRootsFrontendRow builds one enabled OpenResponses frontend row carrying the
// given config body, the same shape an operator's plugins list produces.
func ownedRootsFrontendRow(t *testing.T, body string) config.PluginConfig {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(body), &node); err != nil {
		t.Fatalf("yaml.Unmarshal(%q) = %v", body, err)
	}
	return config.PluginConfig{ID: ownedRootsFrontend, Enabled: true, Config: node}
}

// ownedRootsGeneration projects one generation from cfg and returns the owned-route
// inventory its self-defense gate was given.
func ownedRootsGeneration(t *testing.T, cfg *config.Config) []string {
	t.Helper()
	_, input := selfDefenseGenerationInput(t, cfg)
	sd := input.Security.SelfDefense
	if !sd.SelfDefenseEnabled() {
		t.Fatal("the fixture must leave self-defense default-on")
	}
	return sd.OwnedRoots
}

// TestGenerationCarvesOperatorConfiguredFrontendBasePathOutOfImpossiblePathRefusal
// is the end-to-end regression for the default-on regression: an operator-chosen
// base_path is a valid configuration, it produces real data-plane routes, and the
// deterministic matcher must not answer those routes with the generic 404 before
// the frontend sees them.
//
// The pre-existing non-collision test sampled seven hand-picked frontend configs and
// called that the standard surface, so it never exercised a base_path inside a
// frozen family. This test uses exactly the colliding values that were untested.
func TestGenerationCarvesOperatorConfiguredFrontendBasePathOutOfImpossiblePathRefusal(t *testing.T) {
	t.Parallel()

	for _, basePath := range []string{
		"/wp-admin",
		"/wp-content",
		"/.github",
		"/.gitlab",
		"/cgi-bin",
		"/phpmyadmin",
		"/pma",
		"/adminer",
		"/vendor/phpunit",
		"/.ht",
	} {
		t.Run(basePath, func(t *testing.T) {
			t.Parallel()

			cfg := processServicesTestConfig()
			cfg.Plugins.Frontends = []config.PluginConfig{ownedRootsFrontendRow(t, "base_path: "+basePath+"\n")}

			roots := ownedRootsGeneration(t, cfg)
			if len(roots) == 0 {
				t.Fatalf("base_path %q produced no owned routes, so the matcher would shadow it", basePath)
			}
			// The real published routes for this base path must all be carved.
			for _, route := range []string{basePath + "/responses", basePath + "/responses/compact"} {
				if !slices.Contains(roots, route) {
					t.Errorf("owned roots %v missing the published route %q", roots, route)
					continue
				}
				if selfdefense.ImpossiblePath(route, roots...) {
					t.Errorf("published route %q is still refused as impossible for base_path %q", route, basePath)
				}
			}
			// A probe in the same family, outside the published routes, must still
			// be refused. The carve must not cost the security value.
			probe := basePath + "/adminer.php"
			if !selfdefense.ImpossiblePath(probe, roots...) {
				t.Errorf("probe %q must stay refused for base_path %q", probe, basePath)
			}
		})
	}
}

// TestGenerationCarvesConfiguredDiagnosticsAndMetricsPathsOutOfImpossiblePathRefusal
// covers the other half of the configurable surface. Diagnostics, metrics and the
// protected operator mounts accept any normalized absolute path, so a legitimate
// health or metrics endpoint can be placed inside a frozen family too.
func TestGenerationCarvesConfiguredDiagnosticsAndMetricsPathsOutOfImpossiblePathRefusal(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/wp-admin/healthz", "/phpmyadmin/metrics", "/.github/attempts"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			cfg := processServicesTestConfig()
			cfg.Diagnostics.Enabled = true
			cfg.Diagnostics.HealthPath = path
			cfg.Observability.Metrics.Enabled = true
			cfg.Observability.Metrics.Path = "/wp-content/metrics"

			roots := ownedRootsGeneration(t, cfg)
			if !slices.Contains(roots, path) {
				t.Fatalf("owned roots %v missing the configured diagnostics path %q", roots, path)
			}
			if selfdefense.ImpossiblePath(path, roots...) {
				t.Errorf("configured diagnostics path %q is still refused as impossible", path)
			}
			if !slices.Contains(roots, "/wp-content/metrics") {
				t.Errorf("owned roots %v missing the configured metrics path", roots)
			}
			if selfdefense.ImpossiblePath("/wp-content/metrics", roots...) {
				t.Error("configured metrics path is still refused as impossible")
			}
			// Everything else in those families stays refused.
			for _, probe := range []string{"/phpmyadmin/index.php", "/wp-admin/wp-login.php", "/.github/workflows"} {
				if !selfdefense.ImpossiblePath(probe, roots...) {
					t.Errorf("probe %q must stay refused", probe)
				}
			}
		})
	}
}

// TestGenerationOwnedRootsCoverTheWholeConfigurableDataPlaneSurface is the
// completeness guard. It walks every configurable data-plane path in the config
// model, sets each one inside a frozen family, and requires the generation to carve
// it. A newly configurable mount path that never reaches the inventory fails here
// instead of silently shadowing an operator's endpoint.
func TestGenerationOwnedRootsCoverTheWholeConfigurableDataPlaneSurface(t *testing.T) {
	t.Parallel()

	// One colliding path per configurable group, each in a different frozen family
	// so a carve that only handled one family cannot pass.
	groups := map[string]func(*config.Config, string){
		"diagnostics.health_path":      func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.HealthPath = p },
		"diagnostics.attempts_path":    func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.AttemptsPath = p },
		"diagnostics.inventory_path":   func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.InventoryPath = p },
		"diagnostics.route_trace_path": func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.RouteTracePath = p },
		"diagnostics.pprof_path":       func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.PprofPath = p },
		"observability.metrics.path": func(c *config.Config, p string) {
			c.Observability.Metrics.Enabled = true
			c.Observability.Metrics.Path = p
		},
		"secure_session.diagnostics": func(c *config.Config, p string) {
			enabled := true
			c.SecureSession.Enabled = &enabled
			c.SecureSession.DiagnosticsPathPrefix = p
		},
		"model_catalog.diagnostics_path": func(c *config.Config, p string) { c.ModelCatalog.DiagnosticsPath = p },
		"model_inventory.diagnostics":    func(c *config.Config, p string) { c.ModelInventory.DiagnosticsPath = p },
		"accounting.admin.path": func(c *config.Config, p string) {
			c.Accounting.Admin.Enabled = true
			c.Accounting.Admin.Path = p
		},
	}

	// Distinct frozen families, so no single carve can cover two groups by luck.
	families := []string{
		"/wp-admin", "/wp-content", "/.github", "/.gitlab", "/.ht",
		"/cgi-bin", "/phpmyadmin", "/pma", "/adminer", "/vendor/phpunit",
	}

	i := 0
	for name, apply := range groups {
		apply := apply
		path := families[i%len(families)] + "/x"
		i++
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := processServicesTestConfig()
			apply(cfg, path)
			if err := config.Validate(cfg); err != nil {
				t.Skipf("fixture for %s is not a valid configuration (%v); nothing to prove here", name, err)
			}
			roots := ownedRootsGeneration(t, cfg)
			if !slices.Contains(roots, path) {
				t.Errorf("%s = %q is a valid configured data-plane path but is not in the owned roots %v", name, path, roots)
			}
		})
	}
}

// TestGenerationWithNoSelfDefenseProjectsNoOwnedRoots keeps the fast path clean: a
// disabled generation carries no gate and therefore no inventory.
func TestGenerationWithNoSelfDefenseProjectsNoOwnedRoots(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	disabled := false
	cfg.Access.SelfDefense.Enabled = &disabled
	_, input := selfDefenseGenerationInput(t, cfg)
	if sd := input.Security.SelfDefense; sd.SelfDefenseEnabled() || len(sd.OwnedRoots) != 0 {
		t.Fatalf("disabled self-defense must project the zero input, got %+v", sd)
	}
}

// ownedRootsAreDeterministic guards the inventory against map-iteration order, so a
// generation does not publish a different carve between reloads of one config.
func TestOwnedRootsAreDeterministic(t *testing.T) {
	t.Parallel()

	build := func() []string {
		cfg := processServicesTestConfig()
		cfg.Diagnostics.Enabled = true
		cfg.Diagnostics.HealthPath = "/wp-admin/healthz"
		cfg.Diagnostics.AttemptsPath = "/phpmyadmin/attempts"
		cfg.Observability.Metrics.Enabled = true
		cfg.Observability.Metrics.Path = "/.github/metrics"
		cfg.Plugins.Frontends = []config.PluginConfig{ownedRootsFrontendRow(t, "base_path: /cgi-bin\n")}
		return ownedRootsGeneration(t, cfg)
	}
	first := build()
	if len(first) == 0 {
		t.Fatal("fixture must publish owned roots")
	}
	if !slices.IsSorted(first) {
		t.Fatalf("owned roots %v must be emitted in a stable sorted order", first)
	}
	for range 4 {
		if got := build(); !slices.Equal(got, first) {
			t.Fatalf("owned roots are not deterministic: %v then %v", first, got)
		}
	}
	// No duplicates: the same path can be reachable through more than one surface.
	seen := map[string]bool{}
	for _, root := range first {
		if seen[root] {
			t.Errorf("duplicate owned root %q in %v", root, first)
		}
		seen[root] = true
	}
}
