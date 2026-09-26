package runtimebundle

import (
	"time"

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
func buildSelfDefenseSecurityInput(cand *candidateAssembly, authProviders []httpauth.Provider, now func() time.Time) httpcontract.SelfDefenseSecurityInput {
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
		Probe:           probe,
		Observer:        observer,
		Now:             now,
	}
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
