package security_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	testkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestAdversarial_PluginStderrNeverReachesClientSurface(t *testing.T) {
	t.Parallel()
	const poison = "STDERR_SECRET_sk-or-v1-LEAKME\x1b[31m\x00CONTROL"
	launcher := &processhost.TestLauncher{PID: 9101, StderrPayload: poison}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "stderr-adv",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "stderr-adv"},
		Model:      processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if launcher.LastProcess == nil || launcher.LastProcess.Stderr() == nil {
		t.Fatal("expected launched process stderr from processhost seam")
	}

	fake := &testkit.FakeService{Mode: testkit.ModeSecretTerminal}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "stderr-adv", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "stderr-adv",
		Stderr:     launcher.LastProcess.Stderr(),
	})
	call := lipapi.Call{
		ID:         "stderr-req",
		Session:    lipapi.SessionRef{ALegID: "aleg"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
		Key:     "fake:fake-model",
	}
	stream, err := br.Backend.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	var gotErr error
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			gotErr = err
			break
		}
		blob := string(ev.Kind) + ev.Delta + ev.ErrorMessage + ev.WarningMessage + ev.AssistantRef
		if strings.Contains(blob, "LEAKME") || strings.Contains(blob, "STDERR_SECRET") || strings.Contains(blob, "\x1b") {
			t.Fatalf("event leaked stderr material: %+v", ev)
		}
	}
	if gotErr == nil || errors.Is(gotErr, io.EOF) {
		t.Fatal("expected terminal classified error")
	}
	msg := gotErr.Error()
	for _, bad := range []string{"LEAKME", "STDERR_SECRET", "sk-or-v1-LEAKME", "\x1b"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("client error leaked stderr %q via %q", bad, msg)
		}
	}
}
