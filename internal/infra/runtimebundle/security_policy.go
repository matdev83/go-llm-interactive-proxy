package runtimebundle

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

func validateBackendSecurityProfiles(cfg *config.Config, reg *pluginreg.Registry) error {
	if cfg == nil || reg == nil {
		return nil
	}
	singleUser := cfg.SingleUserLocalMode()
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		factoryID := p.FactoryID()
		profile, ok := reg.BackendSecurityProfile(factoryID)
		if !ok {
			return fmt.Errorf("runtimebundle: backend instance %q (factory %q): missing security profile", p.InstanceID(), factoryID)
		}
		switch profile.CredentialMode {
		case pluginreg.CredentialStatic, pluginreg.CredentialWorkload:
			continue
		case pluginreg.CredentialOAuthUser:
			if !singleUser {
				return fmt.Errorf("runtimebundle: backend instance %q (factory %q): oauth_user credentials require single-user localhost no_auth mode", p.InstanceID(), factoryID)
			}
		case pluginreg.CredentialUnknown, "":
			if !singleUser {
				return fmt.Errorf("runtimebundle: backend instance %q (factory %q): unknown credential mode is not allowed outside single-user localhost mode", p.InstanceID(), factoryID)
			}
		default:
			return fmt.Errorf("runtimebundle: backend instance %q (factory %q): unsupported credential mode %q", p.InstanceID(), factoryID, strings.TrimSpace(string(profile.CredentialMode)))
		}
	}
	return nil
}
