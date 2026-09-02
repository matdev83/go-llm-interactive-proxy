package secretguardcompose

import (
	"fmt"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretaudit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard/engine"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// Environment is the composition-injected process environment port.
type Environment = engine.Environment

// SingleUserOptions configures single-user environment catalog loading.
type SingleUserOptions = engine.SingleUserOptions

// MatcherOptions controls redaction presentation for the composed static matcher.
type MatcherOptions = engine.MatcherOptions

// SecretGuardInputs carries single-user catalog / matcher composition overrides.
type SecretGuardInputs struct {
	SingleUser SingleUserOptions
}

// Input specifies explicit composition inputs for secret-guard runtime assembly.
// It avoids passing BuildOptions, ProcessServices, full config, or registry bags.
type Input struct {
	AccessMode       accessmode.Mode
	Registrations    []lipsdk.Registration
	RuntimeConfig    *secretguard.RuntimeConfig
	Guards           []sdk.Guard
	Environment      Environment
	Inputs           SecretGuardInputs
	SingleUser       SingleUserOptions
	DecisionObserver sdk.Observer
	Logger           *slog.Logger
}

// Output carries assembled runtime extension planes and diagnostic inventory.
type Output struct {
	Plane     extensions.SecretGuardPlane
	Inventory *diag.InventoryExtras
}

// Compose assembles runtime secret-guard components from explicit typed inputs.
func Compose(in Input) (*Output, error) {
	mode := in.AccessMode
	if mode == "" {
		mode = accessmode.ModeSingleUser
	}
	normMode, err := accessmode.NormalizeMode(string(mode))
	if err != nil {
		return nil, fmt.Errorf("secretguardcompose: access mode: %w", err)
	}

	var engineMode engine.AccessMode
	var accessModeStr string
	switch normMode {
	case accessmode.ModeSingleUser:
		engineMode = engine.ModeSingleUser
		accessModeStr = string(accessmode.ModeSingleUser)
	case accessmode.ModeMultiUser:
		engineMode = engine.ModeMultiUser
		accessModeStr = string(accessmode.ModeMultiUser)
	default:
		return nil, fmt.Errorf("secretguardcompose: unsupported access mode %q", in.AccessMode)
	}

	var runtimeCfg secretguard.RuntimeConfig
	if in.RuntimeConfig != nil {
		runtimeCfg = *in.RuntimeConfig
	} else {
		var err error
		runtimeCfg, err = secretguard.ComposeRuntimeConfig(accessModeStr, in.Registrations)
		if err != nil {
			return nil, err
		}
	}

	inputs := in.Inputs.SingleUser
	if !inputs.MatcherConfigured && in.SingleUser.MatcherConfigured {
		inputs = in.SingleUser
	} else if in.SingleUser.IncludePopularEnv || len(in.SingleUser.IncludeEnv) > 0 || len(in.SingleUser.ExcludeEnv) > 0 || in.SingleUser.MinSecretBytes > 0 {
		inputs = in.SingleUser
	}
	singleUser := composeSingleUser(runtimeCfg, inputs)

	featureEnabled := runtimeCfg.Enabled
	src, err := engine.ComposeSource(engineMode, featureEnabled, in.Environment, singleUser)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: secret guard source: %w", err)
	}

	var guards []sdk.Guard
	if len(in.Guards) > 0 {
		guards = append([]sdk.Guard(nil), in.Guards...)
	}

	accessPolicy := sdk.AuditFailClosed
	if runtimeCfg.Enabled {
		accessPolicy = sdk.AuditFailurePolicy(runtimeCfg.AuditFailurePolicy)
	}

	observer := in.DecisionObserver
	if featureEnabled || len(guards) > 0 {
		if !sdk.IsNilObserver(observer) {
			observer = sdk.ChainObservers(accessPolicy, observer)
		} else {
			if in.Logger == nil {
				return nil, fmt.Errorf("runtimebundle: secrets-guard audit requires a non-nil logger")
			}
			slogObs, err := secretaudit.NewSlogObserver(in.Logger)
			if err != nil {
				return nil, fmt.Errorf("runtimebundle: secret guard audit: %w", err)
			}
			observer = sdk.ChainObservers(accessPolicy, slogObs)
		}
	}

	var inventory *diag.InventoryExtras
	if featureEnabled || len(guards) > 0 {
		inventory = &diag.InventoryExtras{
			SecretGuardCatalogEntryCount: src.EntryCount(),
			SecretGuardSourceCategories:  append([]string(nil), src.SourceCategories()...),
			SecretGuardAccessMode:        accessModeStr,
			SecretGuardAction:            runtimeCfg.Action,
		}
	}

	return &Output{
		Plane: extensions.SecretGuardPlane{
			Guards:             guards,
			MatcherResolver:    src.MatcherResolver(),
			DecisionObserver:   observer,
			AuditFailurePolicy: accessPolicy,
			AccessMode:         accessModeStr,
			ConfigVersion:      runtimeCfg.AuditConfigVersion,
		},
		Inventory: inventory,
	}, nil
}

// ComposeSingleUser merges YAML runtime config onto single-user options.
func ComposeSingleUser(runtimeCfg secretguard.RuntimeConfig, inputs SingleUserOptions) SingleUserOptions {
	return composeSingleUser(runtimeCfg, inputs)
}

// composeSingleUser merges YAML runtime config onto composition-seam inputs.
// YAML wins for catalog fields when the feature is enabled. Matcher options from
// inputs win when MatcherConfigured is already set (test/composition override);
// otherwise YAML stamps matcher options.
func composeSingleUser(runtimeCfg secretguard.RuntimeConfig, inputs SingleUserOptions) engine.SingleUserOptions {
	out := inputs
	out.IncludeEnv = append([]string(nil), out.IncludeEnv...)
	out.ExcludeEnv = append([]string(nil), out.ExcludeEnv...)
	if !runtimeCfg.Enabled {
		return out
	}
	matcherOverride := out.MatcherConfigured
	matcher := out.Matcher
	out.IncludePopularEnv = runtimeCfg.IncludePopularEnv
	out.IncludeEnv = append([]string(nil), runtimeCfg.IncludeEnv...)
	out.ExcludeEnv = append([]string(nil), runtimeCfg.ExcludeEnv...)
	out.MinSecretBytes = runtimeCfg.MinSecretBytes
	if matcherOverride {
		out.Matcher = matcher
		out.MatcherConfigured = true
		return out
	}
	out.Matcher = engine.MatcherOptions{
		PreserveKnownPrefixes: runtimeCfg.PreserveKnownPrefixes,
		MaskByte:              runtimeCfg.MaskByte,
	}
	out.MatcherConfigured = true
	return out
}

// ValidateRegistrations validates feature registration uniqueness for secrets-guard.
func ValidateRegistrations(regs []lipsdk.Registration) error {
	_, err := secretguard.EnabledRegistrations(regs)
	if err != nil {
		return fmt.Errorf("runtimebundle: secrets-guard composition: %w", err)
	}
	return nil
}

// EnabledRegistrations returns enabled secrets-guard feature registrations in config order.
func EnabledRegistrations(regs []lipsdk.Registration) ([]lipsdk.Registration, error) {
	return secretguard.EnabledRegistrations(regs)
}
