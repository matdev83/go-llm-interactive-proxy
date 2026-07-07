package standardplugins

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// Re-exports of pluginreg registry types so moved installer/factory files can use
// them unqualified. These are type/const aliases (zero-cost) preserving the same
// identifiers the moved code used when it lived in package pluginreg.

type (
	Registry               = pluginreg.Registry
	BackendFactory         = pluginreg.BackendFactory
	FeatureFactory         = pluginreg.FeatureFactory
	FrontendMount          = pluginreg.FrontendMount
	BackendSecurityProfile = pluginreg.BackendSecurityProfile
	BackendFactoryDeps     = pluginreg.BackendFactoryDeps
	BackendCredentialMode  = pluginreg.BackendCredentialMode
	BackendAccessScope     = pluginreg.BackendAccessScope
	ModelVendorResolver    = pluginreg.ModelVendorResolver
)

const (
	CredentialStatic    = pluginreg.CredentialStatic
	CredentialWorkload  = pluginreg.CredentialWorkload
	CredentialOAuthUser = pluginreg.CredentialOAuthUser
	CredentialNone      = pluginreg.CredentialNone
	CredentialUnknown   = pluginreg.CredentialUnknown

	BackendAccessAny       = pluginreg.BackendAccessAny
	BackendAccessLocalOnly = pluginreg.BackendAccessLocalOnly
)

// EffectiveAPIKeys is the registry-generic key-merge helper that stays in pluginreg;
// alias it so moved credential code can call it unqualified.
var EffectiveAPIKeys = pluginreg.EffectiveAPIKeys

// NewRegistry is aliased so test files that moved from package pluginreg can
// construct registries unqualified.
var NewRegistry = pluginreg.NewRegistry
