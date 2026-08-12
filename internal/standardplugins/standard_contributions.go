package standardplugins

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/alibabatokenplanintl"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openresponsescompat"
	frontanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	frontopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/providerprofiles"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/contrib"
	"gopkg.in/yaml.v3"
)

type standardFrontendContribution struct {
	id          string
	mount       pluginreg.FrontendMount
	routes      FrontendRouteClaims
	diagnostics diag.InstanceDiagnosticProjector
	contract    contrib.ContractSubject
}

type standardBackendContribution struct {
	id               string
	factory          pluginreg.BackendFactory
	lifecycleFactory pluginreg.LifecycleBackendFactory
	profile          pluginreg.BackendSecurityProfile
	source           pluginreg.BackendRegistrationSource
	metadataSource   contrib.RegistrationSource
	essentialOrder   int
	compatibleOrder  int
	family           string
	profileIDs       []string
	contract         contrib.ContractSubject
	diagnostics      diag.InstanceDiagnosticProjector
}

type standardDiagnosticContribution struct {
	id        string
	projector diag.InstanceDiagnosticProjector
}

func standardDiagnosticContributions() []standardDiagnosticContribution {
	return []standardDiagnosticContribution{
		{id: "provider-profile-catalog", projector: ProjectProviderProfileDiagnostics},
	}
}

func StandardContributions() contrib.ContributionSet {
	set := contrib.ContributionSet{}
	for _, c := range standardDiagnosticContributions() {
		set.Diagnostics = append(set.Diagnostics, contrib.DiagnosticFacet{ID: c.id, Declared: c.projector != nil})
	}
	for _, c := range standardFrontendContributions() {
		f := contrib.FrontendContribution{Registration: contrib.FrontendRegistrationFacet{ID: c.id}, Contract: contrib.ContractFacet{Subject: c.contract}}
		if c.routes != nil {
			claims, err := c.routes(c.id, yaml.Node{})
			if err != nil {
				panic(fmt.Errorf("standard frontend contribution %q route claims: %w", c.id, err))
			}
			var claimFacets []contrib.RouteClaimFacet
			for _, cl := range claims {
				claimFacets = append(claimFacets, contrib.RouteClaimFacet{
					Method:      cl.Method,
					Path:        cl.Path,
					OperationID: string(cl.Kind),
				})
			}
			f.Routes = &contrib.RouteFacet{Declared: true, Claims: claimFacets}
		}
		if c.diagnostics != nil {
			f.Diagnostics = &contrib.DiagnosticFacet{ID: c.id + ":diagnostics", Declared: true}
		}
		set.Frontends = append(set.Frontends, f)
	}
	for _, c := range standardBackendContributions(UpstreamAPIKeys{}) {
		b := contrib.BackendContribution{Registration: contrib.BackendRegistrationFacet{ID: c.id, Source: c.metadataSource, EssentialOrder: c.essentialOrder, CompatibleOrder: c.compatibleOrder}, Contract: contrib.ContractFacet{Subject: c.contract}}
		if c.diagnostics != nil {
			b.Diagnostics = &contrib.DiagnosticFacet{ID: c.id + ":diagnostics", Declared: true}
		}
		if c.family != "" {
			b.Compatible = &contrib.CompatibleFamilyFacet{FamilyID: c.family, ProfileIDs: append([]string(nil), c.profileIDs...)}
		}
		set.Backends = append(set.Backends, b)
	}
	return set
}

func DerivedViews() (contrib.Views, error) { return contrib.Derive(StandardContributions()) }

func standardFrontendContributions() []standardFrontendContribution {
	return []standardFrontendContribution{
		{id: frontopenairesponses.ID, mount: frontopenairesponses.Mount, routes: openAIResponsesFrontendRouteClaims, contract: contrib.ContractSubject{ID: frontopenairesponses.ID, Kind: "frontend"}},
		{id: frontopenailegacy.ID, mount: frontopenailegacy.Mount, routes: openAILegacyFrontendRouteClaims, contract: contrib.ContractSubject{ID: frontopenailegacy.ID, Kind: "frontend"}},
		{id: frontanthropic.ID, mount: frontanthropic.Mount, routes: anthropicFrontendRouteClaims, contract: contrib.ContractSubject{ID: frontanthropic.ID, Kind: "frontend"}},
		{id: frontgemini.ID, mount: frontgemini.Mount, routes: geminiFrontendRouteClaims, contract: contrib.ContractSubject{ID: frontgemini.ID, Kind: "frontend"}},
		{id: frontopenresponses.ID, mount: frontopenresponses.Mount, routes: openResponsesFrontendRouteClaims, diagnostics: ProjectOpenResponsesFrontendRows, contract: contrib.ContractSubject{ID: frontopenresponses.ID, Kind: "frontend"}},
	}
}

func standardBackendContributions(keys UpstreamAPIKeys) []standardBackendContribution {
	contributions := []standardBackendContribution{
		{id: openairesponses.ID, factory: backendFactory(keys, backendOpenAIResponses), metadataSource: contrib.SourceBuiltin, source: pluginreg.BackendSourceBuiltin, profile: staticProfile(), essentialOrder: 1, contract: contrib.ContractSubject{ID: openairesponses.ID, Kind: "backend"}},
		{id: openailegacy.ID, factory: backendFactory(keys, backendOpenAILegacy), metadataSource: contrib.SourceBuiltin, source: pluginreg.BackendSourceBuiltin, profile: staticProfile(), essentialOrder: 2, contract: contrib.ContractSubject{ID: openailegacy.ID, Kind: "backend"}},
		{id: anthropic.ID, factory: backendFactory(keys, backendAnthropic), metadataSource: contrib.SourceBuiltin, source: pluginreg.BackendSourceBuiltin, profile: staticProfile(), essentialOrder: 3, contract: contrib.ContractSubject{ID: anthropic.ID, Kind: "backend"}},
		{id: alibabatokenplanintl.ID, factory: backendFactory(keys, backendAlibabaTokenPlanIntl), metadataSource: contrib.SourceBuiltin, source: pluginreg.BackendSourceBuiltin, profile: staticProfile(), essentialOrder: 4, contract: contrib.ContractSubject{ID: alibabatokenplanintl.ID, Kind: "backend"}},
		{id: gemini.ID, factory: backendFactory(keys, backendGemini), metadataSource: contrib.SourceBuiltin, source: pluginreg.BackendSourceBuiltin, profile: staticProfile(), essentialOrder: 5, contract: contrib.ContractSubject{ID: gemini.ID, Kind: "backend"}},
		{id: bedrock.ID, factory: func(n yaml.Node, _ *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return backendBedrock(n, nil, deps.Identity)
		}, metadataSource: contrib.SourceBuiltin, source: pluginreg.BackendSourceBuiltin, profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialWorkload}, essentialOrder: 6, contract: contrib.ContractSubject{ID: bedrock.ID, Kind: "backend"}},
		{id: CustomOpenAIResponsesCompatibleID, lifecycleFactory: openaicompat.LifecycleOpenAIResponsesCompatible, metadataSource: contrib.SourceBuiltinCompatible, source: pluginreg.BackendSourceBuiltinCompatible, profile: staticProfile(), family: string(providerprofiles.FamilyOpenAIResponses), essentialOrder: 7, compatibleOrder: 2, contract: contrib.ContractSubject{ID: CustomOpenAIResponsesCompatibleID, Kind: "backend"}},
		{id: CustomOpenAILegacyCompatibleID, lifecycleFactory: openaicompat.LifecycleOpenAILegacyCompatible, metadataSource: contrib.SourceBuiltinCompatible, source: pluginreg.BackendSourceBuiltinCompatible, profile: staticProfile(), family: string(providerprofiles.FamilyOpenAIChat), essentialOrder: 8, compatibleOrder: 1, contract: contrib.ContractSubject{ID: CustomOpenAILegacyCompatibleID, Kind: "backend"}},
		{id: CustomAnthropicCompatibleID, lifecycleFactory: anthropic.LifecycleAnthropicCompatible, metadataSource: contrib.SourceBuiltinCompatible, source: pluginreg.BackendSourceBuiltinCompatible, profile: staticProfile(), family: string(providerprofiles.FamilyAnthropic), essentialOrder: 9, compatibleOrder: 3, contract: contrib.ContractSubject{ID: CustomAnthropicCompatibleID, Kind: "backend"}},
		{id: CustomOpenResponsesCompatibleID, lifecycleFactory: openresponsescompat.LifecycleOpenResponsesCompatible, metadataSource: contrib.SourceBuiltinCompatible, source: pluginreg.BackendSourceBuiltinCompatible, profile: staticProfile(), family: string(providerprofiles.FamilyOpenResponses), essentialOrder: 10, compatibleOrder: 4, contract: contrib.ContractSubject{ID: CustomOpenResponsesCompatibleID, Kind: "backend"}},
	}
	profiles, err := providerprofiles.EmbeddedCatalog()
	if err != nil {
		panic(err)
	}
	for _, profile := range profiles.Profiles() {
		for i := range contributions {
			if contributions[i].family == string(profile.Family) {
				contributions[i].profileIDs = append(contributions[i].profileIDs, profile.ID)
			}
		}
	}
	return contributions
}

type hostedBackendFactory func(yaml.Node, *http.Client, UpstreamAPIKeys, identity.Config) (execbackend.Backend, error)

func backendFactory(keys UpstreamAPIKeys, f hostedBackendFactory) pluginreg.BackendFactory {
	return func(n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return f(n, upstream, keys, deps.Identity)
	}
}

func staticProfile() pluginreg.BackendSecurityProfile {
	return pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}
}

func frontendRegistrationsFrom(in []standardFrontendContribution) []FrontendRegistration {
	out := make([]FrontendRegistration, 0, len(in))
	for _, c := range in {
		out = append(out, FrontendRegistration{ID: c.id, Mount: c.mount})
	}
	return out
}

func backendRegistrationsFrom(in []standardBackendContribution) []BackendRegistration {
	out := make([]BackendRegistration, 0, len(in))
	for _, c := range in {
		out = append(out, BackendRegistration{ID: c.id, Factory: c.factory, LifecycleFactory: c.lifecycleFactory, Profile: c.profile, Source: c.source})
	}
	return out
}

var (
	standardDiagnosticProjectorsOnce   sync.Once
	standardDiagnosticProjectorsCached []diag.InstanceDiagnosticProjector
)

func StandardDiagnosticProjectors() []diag.InstanceDiagnosticProjector {
	standardDiagnosticProjectorsOnce.Do(func() {
		var out []diag.InstanceDiagnosticProjector
		for _, c := range standardFrontendContributions() {
			if c.diagnostics != nil {
				out = append(out, c.diagnostics)
			}
		}
		for _, c := range standardBackendContributions(UpstreamAPIKeys{}) {
			if c.diagnostics != nil {
				out = append(out, c.diagnostics)
			}
		}
		for _, c := range standardDiagnosticContributions() {
			if c.projector != nil {
				out = append(out, c.projector)
			}
		}
		standardDiagnosticProjectorsCached = out
	})
	// Return a defensive slice copy: callers may reorder or append without
	// mutating the process-wide immutable projector set.
	return append([]diag.InstanceDiagnosticProjector(nil), standardDiagnosticProjectorsCached...)
}
