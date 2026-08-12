package gemini_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type charErrExecutor struct{ err error }

func (e *charErrExecutor) Execute(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	return nil, e.err
}
func (e *charErrExecutor) CancelALeg(context.Context, lipapi.ALegCancelRequest) error { return nil }
func (e *charErrExecutor) WallClock() func() time.Time                                { return nil }

func TestWireErrorCharacterization_executeErrors(t *testing.T) {
	t.Parallel()
	body := policyBody(t)
	cases := []struct {
		name        string
		err         error
		wantStatus  int
		wantGStatus string
		wantMsg     string
	}{
		{
			name:        "policy_denied",
			err:         lipapi.NewPolicyDeniedError("pre_request", "p", "policy_denied", "policy_denied", execerr.PolicyDeniedWireMessage, nil),
			wantStatus:  http.StatusForbidden,
			wantGStatus: "PERMISSION_DENIED",
			wantMsg:     execerr.PolicyDeniedWireMessage,
		},
		{
			name:        "policy_failure_overloaded",
			err:         lipapi.NewPolicyFailureError("tool_policy", "p", "policy_timeout", "policy_failure", "", nil),
			wantStatus:  http.StatusServiceUnavailable,
			wantGStatus: "UNAVAILABLE",
			wantMsg:     execerr.PolicyFailureWireMessage,
		},
		{
			name:        "policy_malformed",
			err:         lipapi.NewPolicyMalformedError("completion", "p", "policy_malformed", "policy_malformed", "", nil),
			wantStatus:  http.StatusInternalServerError,
			wantGStatus: "INTERNAL",
			wantMsg:     execerr.PolicyMalformedWireMessage,
		},
		{
			name:        "capability_reject",
			err:         &lipapi.RejectError{Reason: "missing capability"},
			wantStatus:  http.StatusBadRequest,
			wantGStatus: "INVALID_ARGUMENT",
			wantMsg:     "missing capability",
		},
		{
			name:        "internal_error",
			err:         errors.New("boom"),
			wantStatus:  http.StatusInternalServerError,
			wantGStatus: "INTERNAL",
			wantMsg:     execerr.InternalWireMessage,
		},
		{
			name:        "billing_denied_insufficient_credit",
			err:         fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billing.ErrInsufficientSpendable),
			wantStatus:  http.StatusTooManyRequests,
			wantGStatus: "RESOURCE_EXHAUSTED",
			wantMsg:     execerr.InsufficientCreditWireMessage,
		},
		{
			name:        "billing_unavailable",
			err:         fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, billing.ErrAuthorizationUnavailable),
			wantStatus:  http.StatusServiceUnavailable,
			wantGStatus: "UNAVAILABLE",
			wantMsg:     execerr.BillingUnavailableWireMessage,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &gemini.Handler{
				Exec:                 &charErrExecutor{err: tc.err},
				DefaultRouteSelector: "stub:gemini-2.0-flash",
			}
			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status: got %d want %d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			code, msg, gstatus := decodeGeminiAPIError(t, rr.Body.Bytes())
			if code != tc.wantStatus || msg != tc.wantMsg || gstatus != tc.wantGStatus {
				t.Fatalf("got code=%d msg=%q status=%q want code=%d msg=%q status=%q", code, msg, gstatus, tc.wantStatus, tc.wantMsg, tc.wantGStatus)
			}
			_, msg2, gs2 := frontendpipe.MapGeminiExecuteError(execerr.ClassifyExecute(tc.err))
			if msg2 != tc.wantMsg || gs2 != tc.wantGStatus {
				t.Fatalf("mapper drift: msg=%q status=%q", msg2, gs2)
			}
		})
	}
}

func TestWireErrorCharacterization_malformedRequestJSON(t *testing.T) {
	t.Parallel()
	h := &gemini.Handler{Exec: &charErrExecutor{}, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader([]byte("{")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	code, msg, gstatus := decodeGeminiAPIError(t, rr.Body.Bytes())
	if code != 400 || msg != "invalid request JSON" || gstatus != "INVALID_ARGUMENT" {
		t.Fatalf("got code=%d msg=%q status=%q", code, msg, gstatus)
	}
	var envelope struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != 400 {
		t.Fatalf("error.code: %d", envelope.Error.Code)
	}
}
