package pluginreg

import "testing"

func TestInstallStandardBackendsOn_declaresExplicitNonUnknownPosture(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range StandardBackendBundle(UpstreamAPIKeys{}).Backends {
		id := entry.ID
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			p, ok := reg.BackendSecurityProfile(id)
			if !ok {
				t.Fatalf("missing security profile for bundled backend factory %q", id)
			}
			if p.CredentialMode == CredentialUnknown || p.CredentialMode == "" {
				t.Fatalf("bundled backend %q must declare explicit non-unknown posture, got %q", id, p.CredentialMode)
			}
		})
	}
}
