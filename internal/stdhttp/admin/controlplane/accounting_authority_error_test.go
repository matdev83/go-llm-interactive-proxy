package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
)

func TestWriteAuthorityQueryError_joinedTimeoutPrecedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{
			name:       "timeout_plus_degraded_is_degraded",
			err:        errors.Join(authorityapp.ErrEvaluationTimeout, authorityapp.ErrDegraded),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "degraded",
		},
		{
			name:       "timeout_plus_invalid_query_is_invalid_query",
			err:        errors.Join(authorityapp.ErrEvaluationTimeout, authorityapp.ErrInvalidQuery),
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_query",
		},
		{
			name:       "timeout_plus_unsupported_filter_is_unsupported_filter",
			err:        errors.Join(authorityapp.ErrEvaluationTimeout, authorityapp.ErrUnsupportedFilter),
			wantStatus: http.StatusBadRequest,
			wantError:  "unsupported_filter",
		},
		{
			name:       "timeout_alone_is_unavailable",
			err:        authorityapp.ErrEvaluationTimeout,
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := httptest.NewRecorder()
			writeAuthorityQueryError(rr, tc.err)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status %d want %d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["error"] != tc.wantError {
				t.Fatalf("error %q want %q", body["error"], tc.wantError)
			}
		})
	}
}
