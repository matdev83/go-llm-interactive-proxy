package lipapi_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRecoverablePreOutputError_nil(t *testing.T) {
	t.Parallel()
	if lipapi.RecoverablePreOutputError(nil) != nil {
		t.Fatal("expected nil")
	}
}

func TestRecoverablePreOutputError_preservesCauseChain(t *testing.T) {
	t.Parallel()
	original := errors.New("upstream reset")
	err := lipapi.RecoverablePreOutputError(original)
	if !errors.Is(err, lipapi.ErrRecoverablePreOutput) {
		t.Fatal("expected errors.Is ErrRecoverablePreOutput")
	}
	if !errors.Is(err, original) {
		t.Fatal("expected errors.Is original cause")
	}
}

func TestIsRecoverablePreOutput_sentinelWrapped(t *testing.T) {
	t.Parallel()
	err := lipapi.RecoverablePreOutputError(errors.New("timeout"))
	if !lipapi.IsRecoverablePreOutput(err) {
		t.Fatal("expected recoverable")
	}
}

func TestIsRecoverablePreOutput_upstreamFailure(t *testing.T) {
	t.Parallel()
	err := &lipapi.UpstreamFailure{
		Phase:       lipapi.PhasePreOutput,
		Recoverable: true,
		Reason:      "rate limit",
	}
	if !lipapi.IsRecoverablePreOutput(err) {
		t.Fatal("expected recoverable")
	}
	if !errors.Is(err, lipapi.ErrRecoverablePreOutput) {
		t.Fatal("expected errors.Is ErrRecoverablePreOutput")
	}
}

func TestIsRecoverablePreOutput_postOutputNotRecoverableForRetry(t *testing.T) {
	t.Parallel()
	err := &lipapi.UpstreamFailure{
		Phase:       lipapi.PhasePostOutput,
		Recoverable: true,
	}
	if lipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-output must not match pre-output recoverable predicate")
	}
	if errors.Is(err, lipapi.ErrRecoverablePreOutput) {
		t.Fatal("Unwrap must not expose sentinel for post-output")
	}
}
