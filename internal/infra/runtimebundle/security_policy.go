package runtimebundle

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

func validateBackendSecurityProfiles(cfg *config.Config, reg *pluginreg.Registry) error {
	if cfg == nil || reg == nil {
		return nil
	}
	accessMode, err := cfg.EffectiveAccessMode()
	if err != nil {
		return fmt.Errorf("runtimebundle: backend security profile validation: %w", err)
	}
	multiUser := accessMode == accessmode.ModeMultiUser
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		factoryID := p.FactoryID()
		profile, ok := reg.BackendSecurityProfile(factoryID)
		if !ok {
			return fmt.Errorf(
				"runtimebundle: backend instance %q (factory %q): missing security profile",
				p.InstanceID(),
				factoryID,
			)
		}
		if err := validateBackendAccessScope(profile.AccessScope, p.InstanceID(), factoryID, multiUser); err != nil {
			return err
		}
		if err := validateBackendCredentialMode(profile.CredentialMode, p.InstanceID(), factoryID, multiUser); err != nil {
			return err
		}
	}
	return nil
}

func validateBackendAccessScope(scope pluginreg.BackendAccessScope, instanceID, factoryID string, multiUser bool) error {
	if scope == "" {
		scope = pluginreg.BackendAccessAny
	}
	switch scope {
	case pluginreg.BackendAccessAny:
		return nil
	case pluginreg.BackendAccessLocalOnly:
		if multiUser {
			return fmt.Errorf(
				"%w (instance %q factory %q)",
				ErrLocalOnlyBackendDisallowedMultiUser,
				instanceID,
				factoryID,
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"%w (instance %q factory %q access_scope %q)",
			ErrUnsupportedBackendAccessScope,
			instanceID,
			factoryID,
			strings.TrimSpace(string(scope)),
		)
	}
}

func validateBackendCredentialMode(mode pluginreg.BackendCredentialMode, instanceID, factoryID string, multiUser bool) error {
	switch mode {
	case pluginreg.CredentialStatic, pluginreg.CredentialWorkload, pluginreg.CredentialNone:
		return nil
	case pluginreg.CredentialOAuthUser:
		if multiUser {
			return fmt.Errorf(
				"%w (instance %q factory %q)",
				ErrOAuthUserDisallowedMultiUser,
				instanceID,
				factoryID,
			)
		}
		return nil
	case pluginreg.CredentialUnknown, "":
		if multiUser {
			return fmt.Errorf(
				"%w (instance %q factory %q)",
				ErrUnknownCredentialMultiUser,
				instanceID,
				factoryID,
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"%w (instance %q factory %q mode %q)",
			ErrUnsupportedBackendCredentialMode,
			instanceID,
			factoryID,
			strings.TrimSpace(string(mode)),
		)
	}
}
