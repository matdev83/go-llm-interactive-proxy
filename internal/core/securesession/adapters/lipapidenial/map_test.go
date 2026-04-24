package lipapidenial

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestMapToSessionDenial_Nil(t *testing.T) {
	t.Parallel()
	if MapToSessionDenial(nil) != nil {
		t.Fatalf("expected nil")
	}
}

func TestMapToSessionDenial_ClassifyWithoutStringMatching(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   error
		want lipapi.SessionDenialCode
	}{
		{"missing_principal", domain.ErrMissingPrincipal, lipapi.SessionDeniedMissingPrincipal},
		{"not_found", domain.ErrSessionNotFound, lipapi.SessionDeniedInvalidAuthority},
		{"invalid_token", domain.ErrInvalidResumeToken, lipapi.SessionDeniedInvalidAuthority},
		{"owner", domain.ErrOwnerMismatch, lipapi.SessionDeniedOwnerMismatch},
		{"expired", domain.ErrResumeExpired, lipapi.SessionDeniedResumeExpired},
		{"workspace", domain.ErrWorkspaceDenied, lipapi.SessionDeniedWorkspace},
		{"policy", domain.ErrPolicyUnavailable, lipapi.SessionDeniedPolicyUnavailable},
		{"workspace_unresolved", domain.ErrWorkspaceUnresolved, lipapi.SessionDeniedWorkspace},
		{"storage", domain.ErrStorageUnavailable, lipapi.SessionDeniedStorageUnavailable},
		{"audit", domain.ErrMandatoryAuditFailure, lipapi.SessionDeniedMandatoryAuditFailure},
		{"dup_session", domain.ErrDuplicateSessionID, lipapi.SessionDeniedInvalidAuthority},
		{"dup_fingerprint", domain.ErrDuplicateFingerprint, lipapi.SessionDeniedInvalidAuthority},
		{"transcript_off", domain.ErrTranscriptDisabled, lipapi.SessionDeniedInvalidAuthority},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := MapToSessionDenial(tc.in)
			var sd *lipapi.SessionDenialError
			if !errors.As(out, &sd) {
				t.Fatalf("expected SessionDenialError")
			}
			if got := lipapi.SessionDenialPublicCode(out); string(tc.want) != got {
				t.Fatalf("code: got %q want %q", got, tc.want)
			}
			if sd.Code() != tc.want {
				t.Fatalf("Code(): got %v want %v", sd.Code(), tc.want)
			}
		})
	}
}

func TestMapToSessionDenial_NonEnumeratingPublicMessage(t *testing.T) {
	t.Parallel()

	// Unknown session, bad token, and wrong owner must not give clients different public messages
	// that reveal which case occurred (same safe surface for invalid vs not-found).
	a := MapToSessionDenial(domain.ErrSessionNotFound)
	b := MapToSessionDenial(domain.ErrInvalidResumeToken)
	c := MapToSessionDenial(domain.ErrOwnerMismatch)

	var sa, sb, sc *lipapi.SessionDenialError
	if !errors.As(a, &sa) || !errors.As(b, &sb) || !errors.As(c, &sc) {
		t.Fatalf("expected session denial errors")
	}
	if sa.PublicMessage() != sb.PublicMessage() {
		t.Fatalf("not_found vs invalid public message differ: %q vs %q", sa.PublicMessage(), sb.PublicMessage())
	}
	// Owner mismatch uses the same client-safe phrase as invalid authority in lipapi.
	if sb.PublicMessage() != sc.PublicMessage() {
		t.Fatalf("invalid vs owner public message differ: %q vs %q", sb.PublicMessage(), sc.PublicMessage())
	}
	if sa.InternalReason() == sb.InternalReason() {
		t.Fatalf("internal reasons should differ for operator diagnostics")
	}
	if sb.InternalReason() == sc.InternalReason() {
		t.Fatalf("internal reasons should differ between invalid token and owner mismatch")
	}
}

func TestMapToSessionDenial_UnknownErrorMapsToInvalidAuthority(t *testing.T) {
	t.Parallel()
	in := errors.New("some opaque failure")
	out := MapToSessionDenial(in)
	if lipapi.SessionDenialPublicCode(out) != string(lipapi.SessionDeniedInvalidAuthority) {
		t.Fatalf("expected invalid authority code")
	}
}
