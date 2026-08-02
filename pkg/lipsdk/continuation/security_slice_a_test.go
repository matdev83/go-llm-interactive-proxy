package continuation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func TestResponseIDMaxLengthValidation(t *testing.T) {
	t.Parallel()
	oversized := lipcont.ResponseID("resp_" + strings.Repeat("A", 600))
	if err := oversized.Validate(); err == nil {
		t.Fatal("expected oversized response ID to fail validation")
	}
}

func TestNativeReferenceRedaction(t *testing.T) {
	t.Parallel()
	ref := lipcont.NativeReference{
		Provider: "provider-secret",
		Kind:     "token",
		ID:       "secret-id-123",
		Opaque:   []byte("super-secret-opaque-data"),
	}
	if str := ref.String(); str != "[REDACTED_NATIVE_REF]" {
		t.Fatalf("ref.String() got %q, want %q", str, "[REDACTED_NATIVE_REF]")
	}
	if str := ref.GoString(); str != "[REDACTED_NATIVE_REF]" {
		t.Fatalf("ref.GoString() got %q, want %q", str, "[REDACTED_NATIVE_REF]")
	}
}

func TestLookupUniformErrorClassification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := lipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}

	// Test 1: Malformed ID returns ErrPreviousResponseNotFound directly without store lookup
	malformedStore := &failIfCalledStore{t: t}
	_, err := lipcont.Lookup(ctx, malformedStore, scope, lipcont.ResponseID("invalid_id_format"))
	if !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("malformed ID lookup got %v, want ErrPreviousResponseNotFound", err)
	}

	// Test 2: Incomplete ineligible record mapped to ErrPreviousResponseNotFound
	incompleteStore := stubStore{err: lipcont.ErrIncompleteNotEligible}
	_, err = lipcont.Lookup(ctx, incompleteStore, scope, lipcont.ResponseID("resp_1234567890123456"))
	if !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("incomplete store lookup got %v, want ErrPreviousResponseNotFound", err)
	}

	// Test 3: Record not eligible mapped to ErrPreviousResponseNotFound
	ineligibleStore := stubStore{err: lipcont.ErrRecordNotEligible}
	_, err = lipcont.Lookup(ctx, ineligibleStore, scope, lipcont.ResponseID("resp_1234567890123456"))
	if !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("ineligible store lookup got %v, want ErrPreviousResponseNotFound", err)
	}

	// Test 4: Cycle detected mapped to ErrPreviousResponseNotFound
	cycleStore := stubStore{err: lipcont.ErrCycleDetected}
	_, err = lipcont.Lookup(ctx, cycleStore, scope, lipcont.ResponseID("resp_1234567890123456"))
	if !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("cycle store lookup got %v, want ErrPreviousResponseNotFound", err)
	}

	// Test 5: Depth exceeded mapped to ErrPreviousResponseNotFound
	depthStore := stubStore{err: lipcont.ErrChainDepthExceeded}
	_, err = lipcont.Lookup(ctx, depthStore, scope, lipcont.ResponseID("resp_1234567890123456"))
	if !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("depth store lookup got %v, want ErrPreviousResponseNotFound", err)
	}
}

type failIfCalledStore struct {
	lipcont.Store
	t *testing.T
}

func (s *failIfCalledStore) Get(context.Context, lipcont.Scope, lipcont.ResponseID) (lipcont.ContinuationRecord, error) {
	s.t.Helper()
	s.t.Fatal("store Get should not have been called for malformed response ID")
	return lipcont.ContinuationRecord{}, nil
}

func FuzzResponseIDValidate(f *testing.F) {
	f.Add("resp_1234567890123456")
	f.Add("")
	f.Add("resp_")
	f.Add("resp_bad_base64_!@#$%^&*")
	f.Add(strings.Repeat("A", 1000))

	f.Fuzz(func(t *testing.T, s string) {
		id := lipcont.ResponseID(s)
		_ = id.Validate()
	})
}

func TestMaterializeAmplificationCheckAtEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := lipcont.Scope{TenantID: "t", PrincipalID: "p"}

	// Huge input that exceeds bounds before store lookup
	hugeInput := make([]lipapi.Item, 0, 100)
	for i := 0; i < 100; i++ {
		hugeInput = append(hugeInput, lipapi.Item{
			Kind: lipapi.ItemKindMessage,
			Role: lipapi.RoleUser,
		})
	}

	// Store should not be called if new input itself exceeds MaxMaterializedItems
	mockStore := &failIfCalledStore{t: t}

	_, err := lipcont.Materialize(ctx, lipcont.MaterializeInput{
		Store:    mockStore,
		Scope:    scope,
		StartID:  lipcont.ResponseID("resp_1234567890123456"),
		NewInput: hugeInput,
		Bounds:   lipcont.Bounds{MaxMaterializedItems: 10},
	})
	if !errors.Is(err, lipcont.ErrMaterializedItemsExceeded) {
		t.Fatalf("expected ErrMaterializedItemsExceeded at entry, got %v", err)
	}
}
