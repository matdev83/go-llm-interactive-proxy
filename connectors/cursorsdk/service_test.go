package cursorsdk_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestDescribe_exportsCursorSDK(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if d.PluginID != service.PluginID {
		t.Fatalf("plugin_id=%q", d.PluginID)
	}
	if len(d.Factories) != 1 || d.Factories[0].Kind != service.FactoryKind {
		t.Fatalf("factories=%v", d.Factories)
	}
	fac := d.Factories[0]
	if fac.CredentialMode != backendplugin.CredentialModeStatic {
		t.Fatalf("credential_mode=%v", fac.CredentialMode)
	}
	if fac.AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("access_scope=%v", fac.AccessScope)
	}
	if fac.ProcessSharing != backendplugin.ProcessSharingPerInstance {
		t.Fatalf("process_sharing=%v", fac.ProcessSharing)
	}
}
