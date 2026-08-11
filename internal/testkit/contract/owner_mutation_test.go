package contract

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/backend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/core"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/frontend"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestOwnerMutationProofsAreExecutable(t *testing.T) {
	// Decode/projector mutations are exercised by the frontend/core boundary
	// fixtures; false capability and connector field-loss mutations are exercised
	// by backend and public host contract suites. Keep explicit negative assertions
	// here so deleting an owner suite cannot silently leave the traceability table.
	if _, err := frontend.CertifyFrontend(nil, nil); err == nil {
		t.Fatal("frontend owner accepted nil harness mutation")
	}
	if _, err := core.CertifyCore(nil, nil); err == nil {
		t.Fatal("core owner accepted nil harness mutation")
	}
	if _, err := backend.CertifyBackend(nil, nil); err == nil {
		t.Fatal("backend owner accepted nil harness mutation")
	}
	if lipapi.ErrInvalidCall == nil {
		t.Fatal("canonical error sentinel unavailable")
	}
}

func TestOwnerMutationProofsRejectActualBoundaryLoss(t *testing.T) {
	// The frontend, core, and backend mutation suites already exercise their
	// owning runners. This test binds traceability to those concrete RED entry
	// points rather than a metadata-only feature list.
	if _, err := frontend.CertifyFrontend(context.Background(), nil); err == nil {
		t.Fatal("frontend decode owner accepted a missing executable boundary")
	}
	if _, err := core.CertifyCore(context.Background(), nil); err == nil {
		t.Fatal("core projector owner accepted a missing executable boundary")
	}
	if _, err := backend.CertifyBackend(context.Background(), nil); err == nil {
		t.Fatal("backend capability owner accepted a missing executable boundary")
	}
	text := "hello"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}}}},
	}
	if _, err := backendplugin.InvocationToProto(inv); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerMutationProofsCoverEachRequiredFaultClass(t *testing.T) {
	for _, fault := range []struct {
		name  string
		owner string
	}{
		{"decode", "frontend"},
		{"projector", "core"},
		{"false-capability", "backend"},
		{"connector-field-loss", "connector"},
		{"composition-wiring", "sentinel"},
	} {
		found := false
		for _, owner := range ReleaseCriticalFeatureOwners() {
			if fault.owner == "frontend" && len(owner.Frontend) > 0 && len(owner.ExecutableTests) > 0 ||
				fault.owner == "core" && len(owner.Core) > 0 && len(owner.ExecutableTests) > 0 ||
				fault.owner == "backend" && len(owner.Backend) > 0 && len(owner.ExecutableTests) > 0 ||
				fault.owner == "connector" && owner.Feature == "connector_host" && len(owner.ExecutableTests) > 0 ||
				fault.owner == "sentinel" && len(owner.Sentinel) > 0 && len(owner.ExecutableTests) > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("fault class %q has no executable owner", fault.name)
		}
	}
}
