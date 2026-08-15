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

func TestBackendAccessScopeWireStrings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		scope lipsdk.BackendAccessScope
		want  string
	}{
		{lipsdk.BackendAccessAny, "any"},
		{lipsdk.BackendAccessLocalOnly, "local_only"},
	}
	for _, tc := range cases {
		if string(tc.scope) != tc.want {
			t.Fatalf("BackendAccessScope %s: got %q want %q", tc.want, string(tc.scope), tc.want)
		}
	}
}

func TestBackendSecurityProfileExportedFields(t *testing.T) {
	t.Parallel()
	p := lipsdk.BackendSecurityProfile{CredentialMode: lipsdk.CredentialStatic, AccessScope: lipsdk.BackendAccessLocalOnly}
	if p.CredentialMode != lipsdk.CredentialStatic {
		t.Fatal(p)
	}
	if p.AccessScope != lipsdk.BackendAccessLocalOnly {
		t.Fatal(p)
	}
}

func TestBackendExecutionClassWireStrings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		class lipsdk.BackendExecutionClass
		want  string
	}{
		{lipsdk.BackendExecutionUnknown, ""},
		{lipsdk.BackendExecutionInference, "inference"},
		{lipsdk.BackendExecutionAgentRuntime, "agent_runtime"},
	}
	for _, tc := range cases {
		if string(tc.class) != tc.want {
			t.Fatalf("BackendExecutionClass %s: got %q want %q", tc.want, string(tc.class), tc.want)
		}
	}
}

func TestBackendExecutionProfileExportedFields(t *testing.T) {
	t.Parallel()
	p := lipsdk.BackendExecutionProfile{Class: lipsdk.BackendExecutionInference}
	if p.Class != lipsdk.BackendExecutionInference {
		t.Fatal(p)
	}
	if p.EffectiveClass() != lipsdk.BackendExecutionInference {
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
