package execerr_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestClassifyExecute_reject(t *testing.T) {
	t.Parallel()
	err := &lipapi.RejectError{Reason: "missing thing"}
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindClientReject {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusBadRequest {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != "missing thing" {
		t.Fatalf("message: %q", out.Message)
	}
	if out.Err != err {
		t.Fatalf("Err: want same reject pointer")
	}
}

func TestClassifyExecute_contextLimitExceeded(t *testing.T) {
	t.Parallel()
	err := lipapi.ErrAllCandidatesContextLimitExceeded
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindClientReject {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.ContextLimitExceededWireMessage {
		t.Fatalf("message: %q", out.Message)
	}
	if !lipapi.IsAllCandidatesContextLimitExceeded(out.Err) {
		t.Fatalf("Err should wrap sentinel, got %v", out.Err)
	}
}

func TestClassifyExecute_contextLimitExceeded_wrapped(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(fmt.Errorf("plan: %w", lipapi.ErrAllCandidatesContextLimitExceeded))
	if out.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.ContextLimitExceededWireMessage {
		t.Fatalf("message: %q", out.Message)
	}
}

func TestClassifyExecute_internal(t *testing.T) {
	t.Parallel()
	err := errors.New("backend unavailable")
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindInternalError {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusInternalServerError {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.InternalWireMessage {
		t.Fatalf("message: %q (want non-revealing wire text)", out.Message)
	}
	if out.Err != err {
		t.Fatalf("Err: want original for logging")
	}
}

func TestClassifyExecute_nil(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(nil)
	if out.Kind != execerr.KindInternalError {
		t.Fatalf("kind: %v", out.Kind)
	}
	if out.Status != http.StatusInternalServerError {
		t.Fatalf("status: %d", out.Status)
	}
	if out.Message != execerr.UnknownExecuteErrorMessage {
		t.Fatalf("message: %q", out.Message)
	}
	if out.Err != nil {
		t.Fatalf("Err: %v", out.Err)
	}
}

func TestClassifyExecute_sessionDenial_matrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		err           error
		wantStatus    int
		wantKind      execerr.Kind
		wantCode      string
		wantMsgSubstr string
	}{
		{"missing_principal", lipapi.NewSessionDenialMissingPrincipal("internal"), http.StatusUnauthorized, execerr.KindSessionDenial, string(lipapi.SessionDeniedMissingPrincipal), "identity"},
		{"invalid_authority", lipapi.NewSessionDenialInvalidAuthority("internal"), http.StatusBadRequest, execerr.KindSessionDenial, string(lipapi.SessionDeniedInvalidAuthority), "resumed"},
		{"owner_mismatch", lipapi.NewSessionDenialOwnerMismatch("internal"), http.StatusBadRequest, execerr.KindSessionDenial, string(lipapi.SessionDeniedOwnerMismatch), "resumed"},
		{"resume_expired", lipapi.NewSessionDenialResumeExpired("internal"), http.StatusBadRequest, execerr.KindSessionDenial, string(lipapi.SessionDeniedResumeExpired), "longer"},
		{"workspace", lipapi.NewSessionDenialWorkspace("internal"), http.StatusBadRequest, execerr.KindSessionDenial, string(lipapi.SessionDeniedWorkspace), "workspace"},
		{"policy_unavailable", lipapi.NewSessionDenialPolicyUnavailable("internal"), http.StatusServiceUnavailable, execerr.KindSessionDenial, string(lipapi.SessionDeniedPolicyUnavailable), "policy"},
		{"storage_unavailable", lipapi.NewSessionDenialStorageUnavailable("internal"), http.StatusServiceUnavailable, execerr.KindSessionDenial, string(lipapi.SessionDeniedStorageUnavailable), "storage"},
		{"mandatory_audit", lipapi.NewSessionDenialMandatoryAuditFailure("internal"), http.StatusServiceUnavailable, execerr.KindSessionDenial, string(lipapi.SessionDeniedMandatoryAuditFailure), "recorded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := execerr.ClassifyExecute(tc.err)
			if out.Kind != tc.wantKind {
				t.Fatalf("kind for %v: got %v want %v", tc.wantCode, out.Kind, tc.wantKind)
			}
			if out.Status != tc.wantStatus {
				t.Fatalf("status for %v: got %d want %d", tc.wantCode, out.Status, tc.wantStatus)
			}
			if out.SessionPublicCode != tc.wantCode {
				t.Fatalf("SessionPublicCode: got %q want %q", out.SessionPublicCode, tc.wantCode)
			}
			if tc.wantMsgSubstr != "" && !strings.Contains(out.Message, tc.wantMsgSubstr) {
				t.Fatalf("message for %v: got %q want substring %q", tc.wantCode, out.Message, tc.wantMsgSubstr)
			}
			var sd *lipapi.SessionDenialError
			if !lipapi.IsSessionDenial(out.Err) || !errors.As(out.Err, &sd) {
				t.Fatalf("expected session denial in Err")
			}
			if strings.Contains(out.Message, "internal") {
				t.Fatalf("message must not leak internal reason: %q", out.Message)
			}
		})
	}
}
