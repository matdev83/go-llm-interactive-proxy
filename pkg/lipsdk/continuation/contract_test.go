package continuation_test

import (
	"context"
	"errors"
	"testing"

	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func TestResponseIDValidate(t *testing.T) {
	t.Parallel()
	if err := lipcont.ResponseID("bad").Validate(); err == nil {
		t.Fatal("expected invalid prefix")
	}
	if err := lipcont.ResponseID(lipcont.ResponseIDPrefix + "short").Validate(); err == nil {
		t.Fatal("expected short entropy rejection")
	}
}

func TestScopeEqual(t *testing.T) {
	t.Parallel()
	a := lipcont.Scope{TenantID: "t1", PrincipalID: "p", SessionID: "s"}
	b := lipcont.Scope{TenantID: "t1", PrincipalID: "p", SessionID: "s", ConnectionID: ""}
	if !a.Equal(b) {
		t.Fatal("expected equal scopes")
	}
	c := lipcont.Scope{TenantID: "t2", PrincipalID: "p", SessionID: "s"}
	if a.Equal(c) {
		t.Fatal("expected scopes with different TenantID to not be equal")
	}
}

func TestLookupMapsRecordNotReady(t *testing.T) {
	t.Parallel()
	store := stubStore{err: lipcont.ErrRecordNotReady}
	_, err := lipcont.Lookup(context.Background(), store, lipcont.Scope{PrincipalID: "p"}, lipcont.ResponseID("resp_x"))
	if !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("got %v", err)
	}
}

type stubStore struct {
	lipcont.Store
	err error
}

func (s stubStore) Get(context.Context, lipcont.Scope, lipcont.ResponseID) (lipcont.ContinuationRecord, error) {
	return lipcont.ContinuationRecord{}, s.err
}

func TestDefaultBounds(t *testing.T) {
	t.Parallel()
	b := lipcont.DefaultBounds()
	if b.MaxChainDepth != 64 || b.MaxMaterializedBytes <= 0 {
		t.Fatalf("bounds=%+v", b)
	}
}
