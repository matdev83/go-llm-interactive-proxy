package httpauth_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

func TestPrincipalContext_roundTrip(t *testing.T) {
	t.Parallel()
	want := execview.PrincipalView{
		ID:          "u1",
		DisplayName: "User One",
		Roles:       []string{"admin"},
		Claims:      map[string]string{"tenant": "a"},
	}
	ctx := httpauth.WithPrincipal(context.Background(), want)
	got, ok := httpauth.PrincipalFromContext(ctx)
	if !ok {
		t.Fatal("expected principal")
	}
	if got.ID != want.ID || got.DisplayName != want.DisplayName {
		t.Fatalf("principal mismatch: %+v vs %+v", got, want)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("roles: %+v", got.Roles)
	}
	if got.Claims["tenant"] != "a" {
		t.Fatalf("claims: %+v", got.Claims)
	}
}

func TestPrincipalFromContext_missing(t *testing.T) {
	t.Parallel()
	_, ok := httpauth.PrincipalFromContext(context.Background())
	if ok {
		t.Fatal("expected no principal")
	}
	_, ok = httpauth.PrincipalFromContext(nil) //nolint:staticcheck // SA1012: intentional nil context contract
	if ok {
		t.Fatal("nil context")
	}
}

func TestWithPrincipal_nilParent_usesBackground(t *testing.T) {
	t.Parallel()
	ctx := httpauth.WithPrincipal(nil, execview.PrincipalView{ID: "x"}) //nolint:staticcheck // SA1012: intentional nil parent contract
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	got, ok := httpauth.PrincipalFromContext(ctx)
	if !ok || got.ID != "x" {
		t.Fatalf("principal %+v ok=%v", got, ok)
	}
}
