package pluginreg

import (
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"gopkg.in/yaml.v3"
)

func TestBuildBackend_propagatesUpstreamHTTP(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	var got *http.Client
	id := "probe-upstream-http-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterBackend(id, func(n yaml.Node, upstreamHTTP *http.Client) (any, error) {
		got = upstreamHTTP
		return runtime.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	want := &http.Client{}
	if _, err := reg.BuildBackend(id, yaml.Node{}, want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("upstream HTTP: got %p want %p", got, want)
	}

	if _, err := reg.BuildBackend(id, yaml.Node{}, nil); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("nil upstream: got %p want nil", got)
	}
}
