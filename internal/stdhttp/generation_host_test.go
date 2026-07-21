package stdhttp_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestInitialGeneration_RunWithGenerationHostShutdown(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath:      cfgPath,
		Mode:            runtimebundle.BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() {
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.Background())
		}
	})
	res.Config.Server.Address = addr

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- stdhttp.RunWithGenerationHost(ctx, stdhttp.GenerationHostInput{
			Config:          res.Config,
			Log:             res.Logger,
			Manager:         res.GenerationManager,
			Process:         res.ProcessServices,
			ShutdownTimeout: 5 * time.Second,
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			resp, err := http.Get("http://" + addr + "/any")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusServiceUnavailable {
					ready = true
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		cancel()
		<-errCh
		t.Fatal("generation host never became ready")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithGenerationHost: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timed out")
	}
	if !res.ProcessServices.Closed() {
		t.Fatal("process services must close on host shutdown")
	}
	if _, ok := res.GenerationManager.Acquire(); ok {
		t.Fatal("manager must reject acquire after shutdown")
	}
}
