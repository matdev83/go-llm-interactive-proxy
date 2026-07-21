//go:build !unix

package main

import (
	"testing"
)

// Task 1.5: non-Unix API-only compile/contract (req 1.8, 11.8).

func TestSIGHUP_APIOnlyPlatform(t *testing.T) {
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
	tr := NewRefSIGHUPTrigger()
	stop := StartRefSIGHUPAdapter(nil, tr)
	stop()
	if tr.Delivered() != 0 {
		t.Fatal("API-only stub must not deliver signal triggers")
	}
}

func TestProductionSIGHUPReload_IntegrationRED(t *testing.T) {
	t.Skip("RED until production management-API reload path covers non-Unix platforms")
}
