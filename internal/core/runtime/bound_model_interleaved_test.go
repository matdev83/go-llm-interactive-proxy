package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

type boundInterleavedCatalogRecorder struct {
	inner       *modelcatalog.CatalogResolverImpl
	mu          sync.Mutex
	generations []string
}

func (r *boundInterleavedCatalogRecorder) Resolve(ctx context.Context, c routing.AttemptCandidate, call lipapi.Call, caps lipapi.BackendCaps) modelcatalog.EffectiveFacts {
	facts := r.inner.Resolve(ctx, c, call, caps)
	r.mu.Lock()
	r.generations = append(r.generations, facts.Snapshot.Generation)
	r.mu.Unlock()
	return facts
}

func TestBoundModel_InterleavedContinuationRetainsBoundCatalogOnBareContext(t *testing.T) {
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "bound-model-reservation",
			ReservedAmount: authorityInputAmount(9),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")

	catalog := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	idxA := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"model-1": {Source: modelcatalog.FactSourceCatalog},
	})
	idxB := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"model-1": {Source: modelcatalog.FactSourceCatalog},
	})
	catalog.PublishSnapshot(modelcatalog.Snapshot{Generation: "catalog-A", Index: idxA})
	from.boundCatalog = catalog.BoundView()
	from.boundCatalogOK = true
	catalog.PublishSnapshot(modelcatalog.Snapshot{Generation: "catalog-B", Index: idxB})

	recorder := &boundInterleavedCatalogRecorder{inner: modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		catalog,
	)}
	ex.CatalogResolver = recorder

	rs, err := ex.openInterleavedExecutorContinuation(context.Background(), from, interleavedstate.State{})
	if err != nil {
		t.Fatalf("open continuation: %v", err)
	}
	defer rs.Close()

	recorder.mu.Lock()
	got := append([]string(nil), recorder.generations...)
	recorder.mu.Unlock()
	if len(got) == 0 {
		t.Fatal("catalog resolver was not called")
	}
	for _, generation := range got {
		if generation != "catalog-A" {
			t.Fatalf("interleaved continuation used catalog %q, want bound catalog-A; observations=%v", generation, got)
		}
	}
}
