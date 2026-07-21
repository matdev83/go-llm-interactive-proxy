package configreload_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	mgmtreload "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/configreload"
)

func TestManagement_ServerLifecycle(t *testing.T) {
	t.Parallel()
	coord := newFakeCoordinator("/fixed/startup/config.yaml", func(context.Context, configreload.ReloadTrigger) configreload.ReloadResult {
		return configreload.ReloadResult{Category: configreload.ResultNoop, ActiveGeneration: 1}
	})
	srv, err := mgmtreload.New(mgmtreload.Options{
		Address:     "127.0.0.1:0",
		AuthMode:    mgmtreload.AuthModeBearer,
		BearerToken: "test-management-secret",
	}, coord)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	addr := srv.Addr()
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatalf("expected bound addr, got %q", addr)
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+mgmtreload.StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-management-secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		t.Fatal(err)
	}
}

func TestManagement_RejectsNilCoordinator(t *testing.T) {
	t.Parallel()
	_, err := mgmtreload.New(mgmtreload.Options{Address: "127.0.0.1:0"}, nil)
	if err == nil {
		t.Fatal("want nil coordinator error")
	}
}
