package lipsdk_test

import (
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestHTTPHeaders_APIKeyFrom_bearerAndVendorAliases(t *testing.T) {
	t.Parallel()
	h := lipsdk.DefaultHTTPHeaders()

	t.Run("bearer", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("Authorization", "Bearer sk-test")
		if got := h.APIKeyFrom(hdr); got != "sk-test" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("x-api-key", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("x-api-key", "ant-key")
		if got := h.APIKeyFrom(hdr); got != "ant-key" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("x-goog-api-key", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("x-goog-api-key", "goog-key")
		if got := h.APIKeyFrom(hdr); got != "goog-key" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("api-key", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("api-key", "azure-key")
		if got := h.APIKeyFrom(hdr); got != "azure-key" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("authorization_wins_over_alias", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("Authorization", "Bearer first")
		hdr.Set("x-api-key", "second")
		if got := h.APIKeyFrom(hdr); got != "first" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("basic_falls_through_to_x-api-key", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("Authorization", "Basic dGVzdA==")
		hdr.Set("x-api-key", "ant-key")
		if got := h.APIKeyFrom(hdr); got != "ant-key" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestHTTPHeaders_RouteSelector_firstNonEmpty(t *testing.T) {
	t.Parallel()
	h := lipsdk.HTTPHeaders{Route: []string{lipsdk.HeaderRoute, "X-Custom-Route"}}
	hdr := http.Header{}
	hdr.Set("X-Custom-Route", "alias:model")
	if got := h.RouteSelector(hdr); got != "alias:model" {
		t.Fatalf("got %q", got)
	}
	hdr.Set(lipsdk.HeaderRoute, "default:model")
	if got := h.RouteSelector(hdr); got != "default:model" {
		t.Fatalf("default must win when both present, got %q", got)
	}
}

func TestBearerCredential(t *testing.T) {
	t.Parallel()
	if got := lipsdk.BearerCredential("Bearer tok"); got != "tok" {
		t.Fatalf("got %q", got)
	}
	if got := lipsdk.BearerCredential("Basic x"); got != "" {
		t.Fatalf("basic: got %q", got)
	}
	if got := lipsdk.BearerCredential("Bearer   "); got != "" {
		t.Fatalf("empty bearer: got %q", got)
	}
}
