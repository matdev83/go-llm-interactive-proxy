package tracing

import (
	"context"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	corehttp "github.com/matdev83/go-llm-interactive-proxy/internal/core/http"
)

func TestInit_tracingDisabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	res, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Active {
		t.Fatal("expected tracing inactive")
	}
	if err := res.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoarsePathGroup_spanPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", "/"},
		{"root", "/", "/"},
		{"v1_nested", "/v1/foo/bar", "/v1"},
		{"v1_exact", "/v1", "/v1"},
		{"long_single_segment", "/longsegment-only", "/longsegment-only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := corehttp.CoarsePathGroup(tc.path); got != tc.want {
				t.Fatalf("CoarsePathGroup(%q)=%q want %q", tc.path, got, tc.want)
			}
		})
	}
}

func Test_spanName_coarse(t *testing.T) {
	t.Parallel()
	r, err := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := spanName(r); got != "POST /v1" {
		t.Fatalf("spanName = %q", got)
	}
}
