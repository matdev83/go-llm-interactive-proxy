package commandcodeanthropic_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/commandcode-anthropic/internal/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/commandcode-anthropic/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestLive_CommandCodeAnthropic(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("COMMANDCODE_API_KEY"))
	if key == "" {
		t.Skip("COMMANDCODE_API_KEY not set; skipping live probe")
	}

	svc := service.New()
	req := backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "live-test-anthropic",
		ConfigYAML:  []byte("base_url: https://api.commandcode.ai/provider/v1\n"),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_key": []byte(key)},
		},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := svc.Configure(ctx, req)
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// 1. Live model discovery
	listed, err := inst.ListModels(ctx, 100)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(listed.Models) == 0 {
		t.Fatalf("expected at least 1 model in catalog, got 0")
	}
	var claudeCount int
	for _, m := range listed.Models {
		if strings.Contains(strings.ToLower(m.CanonicalModelID), "claude") {
			claudeCount++
		}
	}
	t.Logf("Live CommandCode Anthropic models found: %d total, %d Claude models", len(listed.Models), claudeCount)

	// 2. Live messages call to CommandCode Anthropic endpoint:
	// Proves endpoint contact and protocol classification. Depending on the plan
	// of the credential (GOAT vs Provider), CommandCode returns either 200 or 403 MODEL_NOT_IN_PLAN.
	cfg, err := service.ParseConfigYAML([]byte("base_url: https://api.commandcode.ai/provider/v1\napi_key: " + key + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	cl := anthropic.NewClient(cfg.BaseURL, cfg.APIKey, hc)

	call := lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Say hello"}}},
		},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.Operation("anthropic.messages"),
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}

	es, err := cl.Open(ctx, call, "claude-haiku-4-5-20251001")
	if err != nil {
		var he *anthropic.HTTPError
		if errors.As(err, &he) {
			t.Logf("Live CommandCode Anthropic endpoint responded with expected HTTP error: status=%d type=%q message=%q", he.StatusCode, he.Type, he.Message)
		} else {
			t.Fatalf("Live Open failed with unexpected error: %v", err)
		}
	} else {
		defer es.Close()
		ev, err := es.Recv(ctx)
		t.Logf("Live CommandCode Anthropic response event: %+v err: %v", ev, err)
	}
}
