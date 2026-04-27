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
		switch profile.CredentialMode {
		case pluginreg.CredentialStatic, pluginreg.CredentialWorkload:
			continue
		case pluginreg.CredentialOAuthUser:
			if multiUser {
				return fmt.Errorf(
					"%w (instance %q factory %q)",
					ErrOAuthUserDisallowedMultiUser,
					p.InstanceID(),
					factoryID,
				)
			}
		case pluginreg.CredentialUnknown, "":
			if multiUser {
				return fmt.Errorf(
					"%w (instance %q factory %q)",
					ErrUnknownCredentialMultiUser,
					p.InstanceID(),
					factoryID,
				)
			}
		default:
			return fmt.Errorf(
				"%w (instance %q factory %q mode %q)",
				ErrUnsupportedBackendCredentialMode,
				p.InstanceID(),
				factoryID,
				strings.TrimSpace(string(profile.CredentialMode)),
			)
		}
	}
	return nil
}
