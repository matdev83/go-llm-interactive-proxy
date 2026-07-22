//go:build !unix

package main

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
)

// Task 5.2: non-Unix API-only production adapter (req 1.8, 11.8).

func TestPlatform_APIOnlyReloadMode(t *testing.T) {
	t.Parallel()
	if PlatformReloadMode() != "api-only" {
		t.Fatalf("mode=%s", PlatformReloadMode())
	}
	if len(ReloadSignals()) != 0 {
		t.Fatal("non-Unix must not register SIGHUP")
	}
	if SignalsOverlap() {
		t.Fatal("unexpected overlap")
	}
}

func TestSignalReload_APIOnlyAdapterNeverDelivers(t *testing.T) {
	t.Parallel()
	sink := &countingSink{}
	adapter := NewSIGHUPAdapter(sink)
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter.Stop()
	adapter.Stop()
	if sink.n != 0 {
		t.Fatalf("API-only adapter must not invoke sink, got %d", sink.n)
	}
}

func TestSignalShutdown_ShutdownSignalsWithoutSIGHUP(t *testing.T) {
	t.Parallel()
	if len(ShutdownSignals()) != 2 {
		t.Fatalf("shutdown=%v", ShutdownSignals())
	}
}

type countingSink struct{ n int }

func (s *countingSink) Reload(context.Context, configreload.ReloadTrigger) configreload.ReloadResult {
	s.n++
	return configreload.ReloadResult{Category: configreload.ResultInternalFailed}
}
