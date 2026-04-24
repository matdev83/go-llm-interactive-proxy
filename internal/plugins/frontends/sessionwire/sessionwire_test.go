package sessionwire_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestApplyAuthoritativeHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, "  sid-1  ")
	h.Set(sessionwire.HeaderResumeToken, " tok-secret ")
	var ref lipapi.SessionRef
	sessionwire.ApplyAuthoritativeHeaders(&ref, h)
	if ref.AuthoritativeSessionID != "sid-1" {
		t.Fatalf("AuthoritativeSessionID: got %q", ref.AuthoritativeSessionID)
	}
	if ref.ResumeToken != "tok-secret" {
		t.Fatalf("ResumeToken: got %q", ref.ResumeToken)
	}
}

func TestApplyAuthoritativeHeaders_nilSafe(t *testing.T) {
	t.Parallel()
	sessionwire.ApplyAuthoritativeHeaders(nil, http.Header{})
	var ref lipapi.SessionRef
	sessionwire.ApplyAuthoritativeHeaders(&ref, nil)
}

func TestHTTPStatusForSessionDenial(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code lipapi.SessionDenialCode
		want int
	}{
		{"missing_principal", lipapi.SessionDeniedMissingPrincipal, 401},
		{"invalid_authority", lipapi.SessionDeniedInvalidAuthority, 400},
		{"owner_mismatch", lipapi.SessionDeniedOwnerMismatch, 400},
		{"resume_expired", lipapi.SessionDeniedResumeExpired, 400},
		{"workspace", lipapi.SessionDeniedWorkspace, 400},
		{"policy_unavailable", lipapi.SessionDeniedPolicyUnavailable, 503},
		{"storage_unavailable", lipapi.SessionDeniedStorageUnavailable, 503},
		{"mandatory_audit", lipapi.SessionDeniedMandatoryAuditFailure, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sessionwire.HTTPStatusForSessionDenial(tc.code); got != tc.want {
				t.Fatalf("code %v: got %d want %d", tc.code, got, tc.want)
			}
		})
	}
}

func TestApplyMetadata(t *testing.T) {
	t.Parallel()
	var ref lipapi.SessionRef
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: "m-sid",
		sessionwire.MetaKeyResumeToken:            "m-tok",
	}
	sessionwire.ApplyMetadata(&ref, meta)
	if ref.AuthoritativeSessionID != "m-sid" || ref.ResumeToken != "m-tok" {
		t.Fatalf("got %+v", ref)
	}
}

func TestWithoutSensitiveToken(t *testing.T) {
	t.Parallel()
	const tok = "raw-resume-bearer"
	msg := "failed resume for " + tok + " on trace"
	got := sessionwire.WithoutSensitiveToken(msg, tok)
	if got == msg {
		t.Fatal("expected redaction")
	}
	if strings.Contains(got, tok) {
		t.Fatalf("token still present: %q", got)
	}
}
