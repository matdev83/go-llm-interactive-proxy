package stdhttp

import (
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestBuildMount_sharedDecodeAdmissionIdentity(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	reg := pluginreg.NewRegistry()
	_, cand := compileTestCandidate(t, cfg, reg)
	if cand.DecodeAdmission() == nil {
		t.Fatal("CompileCandidate DecodeAdmission is nil; want finite limiter")
	}
	shared, ok := cand.DecodeAdmission().(*decodeqos.Limiter)
	if !ok || shared == nil {
		t.Fatalf("DecodeAdmission type = %T, want *decodeqos.Limiter", cand.DecodeAdmission())
	}

	var seen []lipsdk.DecodeAdmission
	recReg := pluginreg.NewRegistry()
	for _, id := range []string{"openai-legacy", "anthropic", "openai-responses", "gemini"} {
		if err := recReg.RegisterFrontend(id, func(_ *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
			seen = append(seen, opts.DecodeAdmission)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: http.NewServeMux(),
		Frontends: HTTPFrontendInput{
			Executor:             cand.Executor(),
			DefaultRouteSelector: "stub:gpt-4o-mini",
			Plugins: []config.PluginConfig{
				{ID: "openai-legacy", Enabled: true},
				{ID: "anthropic", Enabled: true},
				{ID: "openai-responses", Enabled: true},
				{ID: "gemini", Enabled: true},
			},
			DecodeAdmission: cand.DecodeAdmission(),
			Registry:        recReg,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 {
		t.Fatalf("captured %d mount admissions, want 4", len(seen))
	}
	for i, a := range seen {
		got, ok := a.(*decodeqos.Limiter)
		if !ok || got != shared {
			t.Fatalf("mount[%d] DecodeAdmission identity lost: got %#v want %#v", i, a, shared)
		}
	}
}

func TestFrontendMountOptions_decodeAdmissionIdentityPropagates(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.New(2, 1024)
	opts := lipsdk.FrontendMountOptions{DecodeAdmission: limiter}
	got, ok := opts.DecodeAdmission.(*decodeqos.Limiter)
	if !ok || got != limiter {
		t.Fatalf("DecodeAdmission identity lost: got %#v", opts.DecodeAdmission)
	}
}
