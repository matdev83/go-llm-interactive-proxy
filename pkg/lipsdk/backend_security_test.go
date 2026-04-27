package lipsdk

import "testing"

func TestBackendCredentialMode_stringValues(t *testing.T) {
	t.Parallel()
	cases := map[BackendCredentialMode]string{
		CredentialStatic:    "static",
		CredentialWorkload:  "workload",
		CredentialOAuthUser: "oauth_user",
		CredentialUnknown:   "unknown",
	}
	for m, want := range cases {
		if string(m) != want {
			t.Fatalf("BackendCredentialMode %v: want %q", m, want)
		}
	}
}

// ZeroValueBackendSecurityProfile is explicit 'unknown' posture; registry validation
// and startup rules treat unknown as conservative (see spec task 7.x).
func TestBackendSecurityProfile_zeroIsUnknownMode(t *testing.T) {
	t.Parallel()
	var p BackendSecurityProfile
	if p.CredentialMode != "" {
		t.Fatalf("zero profile CredentialMode: got %q, want empty before normalization", p.CredentialMode)
	}
}
