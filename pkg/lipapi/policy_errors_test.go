package lipapi_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPolicyErrorSentinelsAreLipapiPrefixed(t *testing.T) {
	t.Parallel()
	for _, err := range []error{lipapi.ErrPolicyDenied, lipapi.ErrPolicyFailure, lipapi.ErrPolicyMalformed} {
		got := err.Error()
		if len(got) < len("lipapi: ") || got[:len("lipapi: ")] != "lipapi: " {
			t.Fatalf("sentinel %q must be lipapi-prefixed", got)
		}
	}
}

func TestPolicyDecisionErrorRootsAreDistinct(t *testing.T) {
	t.Parallel()
	if errors.Is(lipapi.ErrPolicyDenied, lipapi.ErrPolicyFailure) {
		t.Fatalf("denied must not be failure")
	}
	if errors.Is(lipapi.ErrPolicyDenied, lipapi.ErrPolicyMalformed) {
		t.Fatalf("denied must not be malformed")
	}
	if errors.Is(lipapi.ErrPolicyFailure, lipapi.ErrPolicyMalformed) {
		t.Fatalf("failure must not be malformed")
	}
	for _, other := range []error{
		lipapi.ErrCapabilityReject,
		lipapi.ErrSessionDenial,
		lipapi.ErrInvalidCall,
		lipapi.ErrHookMutation,
	} {
		if errors.Is(lipapi.ErrPolicyDenied, other) || errors.Is(other, lipapi.ErrPolicyDenied) { //nolint:staticcheck // SA1032: intentional bidirectional errors.Is distinctness check
			t.Fatalf("policy denied must be distinct from %v", other)
		}
	}
}

func TestPolicyDecisionErrorClassifiesSeparately(t *testing.T) {
	t.Parallel()
	denied := lipapi.NewPolicyDeniedError("pre_request_admission", "p1", "policy_denied", "policy_denied", "no", nil)
	failure := lipapi.NewPolicyFailureError("pre_request_admission", "p1", "policy_provider_failure", "policy_failure", "down", errors.New("boom"))
	malformed := lipapi.NewPolicyMalformedError("pre_request_admission", "p1", "policy_malformed", "policy_malformed", "bad", errors.New("bad"))

	if !errors.Is(denied, lipapi.ErrPolicyDenied) {
		t.Fatalf("denied must wrap ErrPolicyDenied")
	}
	if !errors.Is(failure, lipapi.ErrPolicyFailure) {
		t.Fatalf("failure must wrap ErrPolicyFailure")
	}
	if !errors.Is(malformed, lipapi.ErrPolicyMalformed) {
		t.Fatalf("malformed must wrap ErrPolicyMalformed")
	}
	if errors.Is(denied, lipapi.ErrPolicyFailure) || errors.Is(denied, lipapi.ErrPolicyMalformed) {
		t.Fatalf("denied must not classify as failure or malformed")
	}
	if errors.Is(failure, lipapi.ErrPolicyDenied) || errors.Is(failure, lipapi.ErrPolicyMalformed) {
		t.Fatalf("failure must not classify as denied or malformed")
	}
	if errors.Is(malformed, lipapi.ErrPolicyDenied) || errors.Is(malformed, lipapi.ErrPolicyFailure) {
		t.Fatalf("malformed must not classify as denied or failure")
	}
}

func TestPolicyDecisionErrorAsExtraction(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("outer: %w", lipapi.NewPolicyDeniedError("s", "p", "r", "c", "m", errors.New("cause")))
	var pde *lipapi.PolicyDecisionError
	if !errors.As(wrapped, &pde) {
		t.Fatalf("errors.As must extract *PolicyDecisionError")
	}
	if pde.Kind != lipapi.PolicyErrorKindDenied {
		t.Fatalf("kind = %q, want denied", pde.Kind)
	}
	if pde.Stage != "s" || pde.ProviderID != "p" || pde.ReasonCode != "r" {
		t.Fatalf("fields lost: %#v", pde)
	}
	if !lipapi.IsPolicyDecisionError(wrapped) {
		t.Fatalf("IsPolicyDecisionError must be true")
	}
	if !lipapi.IsPolicyDenied(wrapped) {
		t.Fatalf("IsPolicyDenied must be true")
	}
	if lipapi.IsPolicyFailure(wrapped) || lipapi.IsPolicyMalformed(wrapped) {
		t.Fatalf("wrapped denied must not be failure or malformed")
	}
}

func TestPolicyDecisionErrorErrorIsClientSafe(t *testing.T) {
	t.Parallel()
	withCause := lipapi.NewPolicyDeniedError("s", "p", "r", "c", "client-safe-msg", errors.New("secret raw prompt payload"))
	got := withCause.Error()
	if got != "client-safe-msg" {
		t.Fatalf("Error() must use client message, got %q", got)
	}
	if !contains(got, "client-safe-msg") {
		t.Fatalf("client message missing")
	}
	if contains(got, "secret raw prompt payload") {
		t.Fatalf("Error() leaked cause text: %q", got)
	}
}

func TestPolicyDecisionErrorDefaultMessages(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind lipapi.PolicyDecisionErrorKind
		want string
	}{
		{lipapi.PolicyErrorKindDenied, "request denied by policy"},
		{lipapi.PolicyErrorKindFailure, "policy decision unavailable"},
		{lipapi.PolicyErrorKindMalformed, "policy decision was malformed"},
	}
	for _, c := range cases {
		err := &lipapi.PolicyDecisionError{Kind: c.kind}
		if err.Error() != c.want {
			t.Fatalf("kind %q default message = %q, want %q", c.kind, err.Error(), c.want)
		}
	}
}

func TestIsPolicyDecisionErrorFalseForUnrelated(t *testing.T) {
	t.Parallel()
	if lipapi.IsPolicyDecisionError(nil) {
		t.Fatalf("nil must not be a policy decision error")
	}
	if lipapi.IsPolicyDecisionError(errors.New("unrelated")) {
		t.Fatalf("unrelated error must not be a policy decision error")
	}
	if lipapi.IsPolicyDecisionError(lipapi.ErrCapabilityReject) {
		t.Fatalf("capability reject must not be a policy decision error")
	}
}

func TestPolicyDecisionErrorKindOf(t *testing.T) {
	t.Parallel()
	err := lipapi.NewPolicyFailureError("s", "p", "r", "c", "m", nil)
	if got := lipapi.PolicyDecisionErrorKindOf(err); got != lipapi.PolicyErrorKindFailure {
		t.Fatalf("kind = %q, want failure", got)
	}
	if got := lipapi.PolicyDecisionErrorKindOf(errors.New("nope")); got != "" {
		t.Fatalf("non-policy error kind must be empty, got %q", got)
	}
}

func TestPolicyDecisionErrorFromNil(t *testing.T) {
	t.Parallel()
	if got := lipapi.PolicyDecisionErrorFrom(nil); got != nil {
		t.Fatalf("From(nil) must return nil")
	}
	err := lipapi.NewPolicyMalformedError("s", "p", "r", "c", "m", nil)
	if got := lipapi.PolicyDecisionErrorFrom(err); got == nil || got.Kind != lipapi.PolicyErrorKindMalformed {
		t.Fatalf("From must return the error, got %#v", got)
	}
}

func TestPolicyDecisionErrorUnwrapNilReceiverSafe(t *testing.T) {
	t.Parallel()
	var nilPDE *lipapi.PolicyDecisionError
	if errs := nilPDE.Unwrap(); errs != nil {
		t.Fatalf("nil receiver Unwrap = %#v, want nil", errs)
	}
	// A boxed typed-nil pointer is a non-nil interface; errors.Is must not panic
	// and must not classify it as a policy error.
	if errors.Is(error(nilPDE), lipapi.ErrPolicyDenied) {
		t.Fatalf("typed-nil must not classify as a policy error")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestNormalizeClientMessageBoundsAndStrips(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"short_passthrough", "safe client msg", "safe client msg"},
		{"trims_surrounding_whitespace", "  hello world\n ", "hello world"},
		{"strips_control_chars", "safe\u0000text\x07with\x01ctrl", "safetextwithctrl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lipapi.NormalizeClientMessage(tc.in); got != tc.want {
				t.Fatalf("NormalizeClientMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeClientMessageBoundsToMaxOnRuneBoundary(t *testing.T) {
	t.Parallel()
	// 600 ASCII bytes -> truncated to exactly MaxClientMessageBytes.
	in := strings.Repeat("a", 600)
	got := lipapi.NormalizeClientMessage(in)
	if len(got) != lipapi.MaxClientMessageBytes {
		t.Fatalf("ascii cut len = %d, want %d", len(got), lipapi.MaxClientMessageBytes)
	}
	// A multi-byte rune straddling the cut must land on a rune start, never mid-rune.
	multi := strings.Repeat("a", lipapi.MaxClientMessageBytes-1) + "😀tail"
	gotMulti := lipapi.NormalizeClientMessage(multi)
	if !utf8.ValidString(gotMulti) {
		t.Fatalf("result not valid UTF-8: %q", gotMulti)
	}
	if len(gotMulti) > lipapi.MaxClientMessageBytes {
		t.Fatalf("multi-byte cut exceeded bound: %d", len(gotMulti))
	}
}

func TestPolicyDeniedErrorConstructorBoundsClientMessage(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 600)
	err := lipapi.NewPolicyDeniedError(
		"pre_request", "p1", "policy_denied", "policy_denied",
		long,
		errors.New("raw secret cause with prompt text"),
	)
	pde := lipapi.PolicyDecisionErrorFrom(err)
	if pde == nil {
		t.Fatal("expected PolicyDecisionError")
	}
	if len(pde.ClientMessage) > lipapi.MaxClientMessageBytes {
		t.Fatalf("ClientMessage not bounded: len=%d", len(pde.ClientMessage))
	}
	if pde.ClientMessage != long[:lipapi.MaxClientMessageBytes] {
		t.Fatalf("ClientMessage = %q, want first %d bytes", pde.ClientMessage, lipapi.MaxClientMessageBytes)
	}
	// Cause must be preserved verbatim for operator diagnostics.
	if pde.Cause == nil || !strings.Contains(pde.Cause.Error(), "raw secret cause with prompt text") {
		t.Fatalf("Cause must preserve raw cause text, got %#v", pde.Cause)
	}
}

func TestPolicyDecisionErrorConstructorsStripControlChars(t *testing.T) {
	t.Parallel()
	ctrl := "safe\u0000text\x07with\x01ctrl"
	for _, err := range []error{
		lipapi.NewPolicyDeniedError("s", "p", "r", "c", ctrl, nil),
		lipapi.NewPolicyFailureError("s", "p", "r", "c", ctrl, nil),
		lipapi.NewPolicyMalformedError("s", "p", "r", "c", ctrl, nil),
	} {
		pde := lipapi.PolicyDecisionErrorFrom(err)
		if pde == nil {
			t.Fatal("expected PolicyDecisionError")
		}
		if strings.ContainsAny(pde.ClientMessage, "\x00\x01\x07") {
			t.Fatalf("control characters survived in ClientMessage: %q", pde.ClientMessage)
		}
	}
}

func TestPolicyDecisionErrorEmptyClientMessageFallsBackToDefault(t *testing.T) {
	t.Parallel()
	err := lipapi.NewPolicyDeniedError("s", "p", "r", "c", "", nil)
	if got := err.Error(); got != "request denied by policy" {
		t.Fatalf("empty ClientMessage must fall back to kind default, got %q", got)
	}
}
