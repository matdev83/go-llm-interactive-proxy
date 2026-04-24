package lipapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const distinctiveFakeResumeToken = "DISTINCTIVE_FAKE_RESUME_TOKEN_XYZ_12345"

func TestSessionDenialError_codesAndUnwrap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want lipapi.SessionDenialCode
	}{
		{"missing_principal", lipapi.NewSessionDenialMissingPrincipal(""), lipapi.SessionDeniedMissingPrincipal},
		{"invalid_authority", lipapi.NewSessionDenialInvalidAuthority(""), lipapi.SessionDeniedInvalidAuthority},
		{"owner_mismatch", lipapi.NewSessionDenialOwnerMismatch(""), lipapi.SessionDeniedOwnerMismatch},
		{"expired_resume", lipapi.NewSessionDenialResumeExpired(""), lipapi.SessionDeniedResumeExpired},
		{"workspace", lipapi.NewSessionDenialWorkspace(""), lipapi.SessionDeniedWorkspace},
		{"policy_unavailable", lipapi.NewSessionDenialPolicyUnavailable(""), lipapi.SessionDeniedPolicyUnavailable},
		{"storage_unavailable", lipapi.NewSessionDenialStorageUnavailable(""), lipapi.SessionDeniedStorageUnavailable},
		{"mandatory_audit", lipapi.NewSessionDenialMandatoryAuditFailure(""), lipapi.SessionDeniedMandatoryAuditFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sd *lipapi.SessionDenialError
			if !errors.As(tc.err, &sd) {
				t.Fatalf("expected SessionDenialError, got %T", tc.err)
			}
			if sd.Code() != tc.want {
				t.Fatalf("Code: got %q want %q", sd.Code(), tc.want)
			}
			if !errors.Is(tc.err, lipapi.ErrSessionDenial) {
				t.Fatalf("expected errors.Is(_, ErrSessionDenial)")
			}
			if lipapi.SessionDenialPublicCode(tc.err) == "" {
				t.Fatal("expected non-empty public code")
			}
		})
	}
}

func TestSessionDenialError_Error_doesNotLeakInternalReasonOrResumeToken(t *testing.T) {
	t.Parallel()
	internal := "probe: " + distinctiveFakeResumeToken + " detail"
	err := lipapi.NewSessionDenialInvalidAuthority(internal)
	msg := err.Error()
	if strings.Contains(msg, distinctiveFakeResumeToken) {
		t.Fatalf("Error() leaked token: %q", msg)
	}
	var sd *lipapi.SessionDenialError
	if !errors.As(err, &sd) {
		t.Fatal("expected SessionDenialError")
	}
	if sd.InternalReason() != internal {
		t.Fatalf("InternalReason not preserved")
	}
	// fmt path should still avoid token in default formatting of error chain
	joined := fmt.Errorf("wrap: %w", err)
	if strings.Contains(joined.Error(), distinctiveFakeResumeToken) {
		t.Fatalf("wrapped Error leaked token: %q", joined.Error())
	}
}

func TestSessionRef_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	ref := lipapi.SessionRef{
		ClientSessionID:        "client-hint",
		ContinuityKey:          "ck",
		ALegID:                 "aleg",
		AuthoritativeSessionID: "proxy-sess",
		ResumeToken:            "bearer-secret",
	}
	b, jerr := json.Marshal(ref)
	if jerr != nil {
		t.Fatal(jerr)
	}
	var got lipapi.SessionRef
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != ref {
		t.Fatalf("round trip: %+v vs %+v", got, ref)
	}
}
