package runtimebundle_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

func jsonBodyRestart(model string, input string, stream bool, meta map[string]string) string {
	m := map[string]any{"model": model, "input": input, "stream": stream}
	if len(meta) > 0 {
		m["metadata"] = meta
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type clockHolder struct{ t time.Time }

func TestSecureSessionE2E_sqliteRestart_resumeSurvivesProcessClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	contPath := filepath.Join(dir, "cont.db")
	ssPath := filepath.Join(dir, "ss.db")
	ch := &clockHolder{t: time.Unix(40000, 0).UTC()}
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "stub:gpt-4o-mini",
		},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			Store:      "sqlite",
			SQLitePath: contPath,
		},
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "sqlite",
			SQLitePath:          ssPath,
			TokenFingerprintKey: testSecureKey32,
			AuditDurability:     "best_effort",
			ResumeWindow:        "24h",
		},
	}
	log := testkit.DiscardLogger()
	buildOpts := func() *runtimebundle.BuildOptions {
		return &runtimebundle.BuildOptions{
			PluginRegistry: pluginreg.NewRegistry(),
			Clock:          func() time.Time { return ch.t },
		}
	}

	b1, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, buildOpts())
	if err != nil {
		t.Fatal(err)
	}
	injectStubBackend(t, b1)
	s1, sid, tok, aleg := runCreatePhase(t, b1)
	s1.Close()
	closeBuilt(t, b1)

	ch.t = ch.t.Add(2 * time.Minute)
	b2, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, buildOpts())
	if err != nil {
		t.Fatal(err)
	}
	injectStubBackend(t, b2)
	s2 := runResumePhase(t, b2, sid, tok, aleg)
	s2.Close()
	closeBuilt(t, b2)

	ch.t = ch.t.Add(2 * time.Minute)
	b3, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, buildOpts())
	if err != nil {
		t.Fatal(err)
	}
	injectStubBackend(t, b3)
	s3 := runWrongOwnerPhase(t, b3, sid, tok)
	s3.Close()
	closeBuilt(t, b3)
}

func runCreatePhase(t *testing.T, b *runtimebundle.Built) (*httptest.Server, string, string, string) {
	t.Helper()
	h := &openairesponses.Handler{Exec: b.Executor, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipalRestart(h, "restart-owner"))
	srv := httptest.NewServer(mux)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBodyRestart("gpt-4o-mini", "boot", false, nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		bb, _ := io.ReadAll(resp.Body)
		srv.Close()
		t.Fatalf("create status %d body=%s", resp.StatusCode, bb)
	}
	sid := strings.TrimSpace(resp.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	tok := strings.TrimSpace(resp.Header.Get(sessionwire.HeaderResumeToken))
	_, _ = io.ReadAll(resp.Body)
	if sid == "" || tok == "" {
		srv.Close()
		t.Fatalf("missing carriers sid=%q tok=%q", sid, tok)
	}
	rec, err := b.SecureSessionStore.LoadByID(context.Background(), domain.SessionID(sid))
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	if rec.Owner.ID != "restart-owner" {
		srv.Close()
		t.Fatalf("owner: want restart-owner got %q", rec.Owner.ID)
	}
	return srv, sid, tok, rec.ALegID
}

func runResumePhase(t *testing.T, b *runtimebundle.Built, sid, tok, wantALeg string) *httptest.Server {
	t.Helper()
	h := &openairesponses.Handler{Exec: b.Executor, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipalRestart(h, "restart-owner"))
	srv := httptest.NewServer(mux)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBodyRestart("gpt-4o-mini", "resume", false, meta)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		bb, _ := io.ReadAll(resp.Body)
		srv.Close()
		t.Fatalf("resume status %d body=%s", resp.StatusCode, bb)
	}
	_, _ = io.ReadAll(resp.Body)
	rec, err := b.SecureSessionStore.LoadByID(context.Background(), domain.SessionID(sid))
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	if rec.ALegID != wantALeg {
		srv.Close()
		t.Fatalf("A-leg after restart: want %q got %q", wantALeg, rec.ALegID)
	}
	return srv
}

func runWrongOwnerPhase(t *testing.T, b *runtimebundle.Built, sid, tok string) *httptest.Server {
	t.Helper()
	h := &openairesponses.Handler{Exec: b.Executor, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipalRestart(h, "other-owner"))
	srv := httptest.NewServer(mux)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBodyRestart("gpt-4o-mini", "bad", false, meta)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		bb, _ := io.ReadAll(resp.Body)
		srv.Close()
		t.Fatalf("wrong owner want 400 got %d body=%s", resp.StatusCode, bb)
	}
	_, _ = io.ReadAll(resp.Body)
	return srv
}

func closeBuilt(t *testing.T, b *runtimebundle.Built) {
	t.Helper()
	for _, c := range b.Closers {
		if c == nil {
			continue
		}
		if err := c(); err != nil {
			t.Fatal(err)
		}
	}
}

func injectStubBackend(t *testing.T, b *runtimebundle.Built) {
	t.Helper()
	if b.Executor.Backends == nil {
		b.Executor.Backends = map[string]execbackend.Backend{}
	}
	b.Executor.Backends["stub"] = execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
			_ = ctx
			_ = call
			_ = cand
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "sqlite-e2e"},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
	}
}

func withPrincipalRestart(h http.Handler, pid string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := execview.WithPrincipal(r.Context(), execview.PrincipalView{ID: pid})
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
