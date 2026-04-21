package continuity_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
	lipsdkc "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuity"
)

func TestSDKStore_roundTripALeg(t *testing.T) {
	t.Parallel()
	inner := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var _ lipsdkc.Store = continuity.SDKStore(inner)
	s := continuity.SDKStore(inner)
	ctx := context.Background()
	r, err := s.CreateALeg(ctx, "k-sdk")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetALeg(ctx, r.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ALegID != r.ALegID {
		t.Fatalf("got %q want %q", got.ALegID, r.ALegID)
	}
}
