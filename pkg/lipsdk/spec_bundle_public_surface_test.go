package lipsdk_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

// Black-box anchors for exported registration and security metadata (specification bundle).
func TestBackendCredentialModeWireStrings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode lipsdk.BackendCredentialMode
		want string
	}{
		{lipsdk.CredentialStatic, "static"},
		{lipsdk.CredentialWorkload, "workload"},
		{lipsdk.CredentialOAuthUser, "oauth_user"},
		{lipsdk.CredentialNone, "none"},
		{lipsdk.CredentialUnknown, "unknown"},
	}
	for _, tc := range cases {
		if string(tc.mode) != tc.want {
			t.Fatalf("BackendCredentialMode %s: got %q want %q", tc.want, string(tc.mode), tc.want)
		}
	}
}

func TestBackendSecurityProfileExportedFields(t *testing.T) {
	t.Parallel()
	p := lipsdk.BackendSecurityProfile{CredentialMode: lipsdk.CredentialStatic}
	if p.CredentialMode != lipsdk.CredentialStatic {
		t.Fatal(p)
	}
}

func TestFeatureBundleSchemaVersionConstant(t *testing.T) {
	t.Parallel()
	if feature.SchemaVersionV1 != 1 {
		t.Fatalf("SchemaVersionV1 = %d", feature.SchemaVersionV1)
	}
	var b feature.FeatureBundle
	b.SchemaVersion = feature.SchemaVersionV1
	if b.SchemaVersion != 1 {
		t.Fatal(b)
	}
}
