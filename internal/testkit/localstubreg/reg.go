package localstubreg

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/localstub"
	"gopkg.in/yaml.v3"
)

// RegisterInProcess registers the in-process local-stub factory for tests that
// cannot launch the connectors/localstub executable. Production composition
// must not call this; serve uses discovered plugins instead.
func RegisterInProcess(reg *pluginreg.Registry) error {
	if reg == nil {
		return nil
	}
	if reg.HasBackend(localstub.ID) {
		return nil
	}
	return reg.RegisterBackendWithProfile(localstub.ID, func(n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return localstub.NewFromYAML(n)
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialNone,
		AccessScope:    pluginreg.BackendAccessAny,
	})
}
