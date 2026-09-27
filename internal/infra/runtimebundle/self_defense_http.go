package runtimebundle

import (
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	stdauth "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// buildSelfDefenseSecurityInput projects one generation's ingress self-defense
// into the cycle-neutral standard data-plane security contract.
//
// The projection is built whenever self-defense is enabled, independently of the
// fixed GeoIP enforcement policy: self-defense reuses the already compiled
// GeoIP client-IP source and trusted proxies (requirement 8.5) and therefore
// needs no GeoIP country database, so a deployment with country/CIDR enforcement
// off still gets the shared forwarding-trust boundary. The GeoIP gate itself
// stays gated on its own policy.
//
// Policy is the immutable generation policy; State is the borrowed
// process-lifetime adaptive state, which this projection never closes or resizes;
// the credential probe is built once from the very provider slice the transport-auth
// chain runs; and the observer is the process metrics bundle's one bounded
// self-defense collector, borrowed so several generations never open duplicate
// series. The observer is nil when observability.metrics is disabled, which changes
// no security decision, state mutation or response: metrics are non-authoritative.
//
// A disabled self-defense returns the zero projection, which is the structural
// fast path: no gate, no matcher, no state lookup, no auth observation.
func buildSelfDefenseSecurityInput(cand *candidateAssembly, frozen *config.Config, authProviders []httpauth.Provider, now func() time.Time) httpcontract.SelfDefenseSecurityInput {
	if cand == nil || cand.security.selfDefense == nil || !cand.security.selfDefense.Enabled() {
		return httpcontract.SelfDefenseSecurityInput{}
	}
	policy := cand.security.selfDefense.Policy()
	var probe httpcontract.CredentialProbe
	if disposition := stdauth.NewCredentialPresenceDispositionProbe(authProviders); disposition != nil {
		probe = disposition
	}
	var observer httpcontract.SelfDefenseObserver
	if cand.process.metrics != nil {
		observer = cand.process.metrics.SelfDefense
	}
	return httpcontract.SelfDefenseSecurityInput{
		Policy:          &policy,
		State:           cand.process.ingressDefense,
		Resolver:        selfDefenseResolverConfig(cand),
		ImpossiblePaths: cand.security.selfDefense.ImpossiblePaths(),
		OwnedRoots:      selfDefenseOwnedRoots(frozen),
		Probe:           probe,
		Observer:        observer,
		Now:             now,
	}
}

// selfDefenseOwnedRoots returns the data-plane routes this generation publishes, so
// the deterministic impossible-path matcher never refuses a route the router would
// actually serve.
//
// The frozen families are heuristic probe prefixes, but the data-plane path surface
// is operator-configurable: OpenResponses accepts any normalized absolute non-root
// base_path, and the diagnostics, metrics and protected operator mounts accept any
// normalized absolute path. Those spaces overlap, so without this inventory a valid,
// already-compiled configuration such as base_path=/wp-admin would have its real
// routes answered with the generic 404 before the frontend ever saw them.
//
// Both halves come from the chokepoints that already own them: the configured paths
// from [config.ConfiguredDataPlanePaths], the same collector that rejects duplicate
// and nested mount paths, and the frontend paths from the registered route-claims
// providers, the same seam that detects canonical route takeover before mounting. A
// newly configurable mount path or a newly mounted frontend therefore becomes visible
// here without a second registration.
func selfDefenseOwnedRoots(frozen *config.Config) []string {
	if frozen == nil {
		return nil
	}
	seen := make(map[string]struct{}, 16)
	roots := make([]string, 0, 16)
	add := func(path string) {
		path = strings.TrimSuffix(strings.TrimSpace(path), "/")
		if path == "" || path == "/" {
			return
		}
		if _, duplicate := seen[path]; duplicate {
			return
		}
		seen[path] = struct{}{}
		roots = append(roots, path)
	}
	for _, path := range config.ConfiguredDataPlanePaths(frozen) {
		add(path)
	}
	providers := standardplugins.StandardFrontendRouteClaims()
	for _, p := range frozen.Plugins.Frontends {
		if !p.Enabled {
			continue
		}
		provider := providers[p.FactoryID()]
		if provider == nil {
			continue
		}
		claims, err := provider(p.InstanceID(), p.Config)
		if err != nil {
			// An invalid frontend config is rejected by configuration validation
			// and fails the candidate; it is not this projection's error to report.
			continue
		}
		for _, claim := range claims {
			normalized, err := claim.NormalizedClaim()
			if err != nil {
				continue
			}
			add(normalized.Path)
		}
	}
	// Canonical order so one configuration always publishes the same inventory,
	// independent of the order the configured paths and the claims providers were
	// visited in.
	slices.Sort(roots)
	return roots
}

// selfDefenseResolverConfig reuses the compiled GeoIP client-address trust
// configuration verbatim, so self-defense creates no second forwarding parser and
// no second independently configurable trust boundary.
func selfDefenseResolverConfig(cand *candidateAssembly) httpcontract.GeoIPResolverConfig {
	compiled := cand.security.geoip
	if compiled == nil {
		return httpcontract.GeoIPResolverConfig{}
	}
	return httpcontract.GeoIPResolverConfig{
		Source:         compiled.ClientIPSource(),
		TrustedProxies: compiled.TrustedProxies(),
	}
}
