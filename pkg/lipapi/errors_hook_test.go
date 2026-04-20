package lipapi_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestIsHookMutation(t *testing.T) {
	t.Parallel()
	err := &lipapi.HookMutationError{HookID: "h1", Details: "bad part", Cause: lipapi.ErrInvalidCall}
	if !lipapi.IsHookMutation(err) {
		t.Fatal("expected IsHookMutation true")
	}
	if !lipapi.IsHookMutation(errors.Join(lipapi.ErrHookMutation, err)) {
		t.Fatal("expected wrapped mutation")
	}
	if lipapi.IsHookMutation(errors.New("other")) {
		t.Fatal("expected false")
	}
}
