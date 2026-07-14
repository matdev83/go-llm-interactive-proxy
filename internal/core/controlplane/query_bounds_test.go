package controlplane_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestQueryServiceRejectsTooBroadUsageBeforeStoreAccess(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	_, err := qs.Usage(context.Background(), cp.UsageQuery{Limit: 10})
	if !errors.Is(err, controlplane.ErrTooBroad) {
		t.Fatalf("got %v", err)
	}
	if store.usageCalled {
		t.Fatal("too-broad usage query must not reach store")
	}
}

func TestQueryServiceAcceptsBoundedUsageWithScopeFilter(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	_, err := qs.Usage(context.Background(), cp.UsageQuery{
		Common: cp.CommonFilters{Scope: cp.ScopeFilters{TenantID: scope.Known("ten-1")}},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if !store.usageCalled {
		t.Fatal("bounded usage query must reach store")
	}
}
