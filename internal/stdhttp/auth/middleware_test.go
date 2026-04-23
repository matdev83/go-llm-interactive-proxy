package auth_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

func TestMiddleware_noProviders_passthrough(t *testing.T) {
	t.Parallel()
	var saw bool
	h := auth.Middleware(nil, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = true
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot || !saw {
		t.Fatalf("code=%d saw=%v", rec.Code, saw)
	}
}

func TestMiddleware_principalPropagates(t *testing.T) {
	t.Parallel()
	want := execview.PrincipalView{ID: "alice"}
	p := stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: want}}
	var gotCtx context.Context
	h := auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	pg, ok := httpauth.PrincipalFromContext(gotCtx)
	if !ok || pg.ID != "alice" {
		t.Fatalf("principal %+v ok=%v", pg, ok)
	}
}

func TestMiddleware_reject(t *testing.T) {
	t.Parallel()
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:       httpauth.TypeReject,
		HTTPStatus: http.StatusForbidden,
		Body:       []byte("nope"),
	}}
	var inner bool
	h := auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		inner = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusForbidden || rec.Body.String() != "nope" || inner {
		t.Fatalf("code=%d body=%q inner=%v", rec.Code, rec.Body.String(), inner)
	}
}

func TestMiddleware_annotate_allowList(t *testing.T) {
	t.Parallel()
	rh := http.Header{}
	rh.Set("Vary", "Accept-Encoding")
	rh.Set("Set-Cookie", "session=evil; Path=/")
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:            httpauth.TypeAnnotate,
		ResponseHeaders: rh,
	}}
	rec := httptest.NewRecorder()
	auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
		t.Fatalf("Vary: %q", got)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie should be stripped, got %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestMiddleware_annotate_allowList_logsDropped(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	rh := http.Header{}
	rh.Set("Set-Cookie", "x=1")
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:            httpauth.TypeAnnotate,
		ResponseHeaders: rh,
	}}
	auth.Middleware(log, []httpauth.Provider{p}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(buf.String(), "Set-Cookie") {
		t.Fatalf("expected warn log about dropped header, got %q", buf.String())
	}
}

func TestMiddleware_challenge(t *testing.T) {
	t.Parallel()
	hd := http.Header{}
	hd.Set("WWW-Authenticate", `Bearer realm="lip"`)
	p := stubProvider{res: httpauth.AuthenticationResult{
		Type:       httpauth.TypeChallenge,
		HTTPStatus: http.StatusUnauthorized,
		Headers:    hd,
	}}
	rec := httptest.NewRecorder()
	auth.Middleware(nil, []httpauth.Provider{p}, http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("missing challenge header")
	}
}

func TestMiddleware_providerError_failClosed(t *testing.T) {
	t.Parallel()
	p := errProvider{}
	rec := httptest.NewRecorder()
	auth.Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner should not run")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestMiddleware_chain_ordering(t *testing.T) {
	t.Parallel()
	var order []int
	p1 := orderProvider{n: 1, order: &order, res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}}
	p2 := orderProvider{n: 2, order: &order, res: httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "from-2"},
	}}
	p3 := orderProvider{n: 3, order: &order, res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}}
	var pid string
	h := auth.Middleware(nil, []httpauth.Provider{p1, p2, p3}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := httpauth.PrincipalFromContext(r.Context())
		pid = p.ID
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("order %v", order)
	}
	if pid != "from-2" {
		t.Fatalf("principal id %q", pid)
	}
}

type testCollisionKey struct{}

func TestEnsureContextPrincipal_copiesFromParent(t *testing.T) {
	t.Parallel()
	parent := httpauth.WithPrincipal(context.Background(), execview.PrincipalView{ID: "p"})
	child := context.WithValue(context.Background(), testCollisionKey{}, 1)
	out := auth.EnsureContextPrincipal(parent, child)
	p, ok := httpauth.PrincipalFromContext(out)
	if !ok || p.ID != "p" {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
}

func TestEnsureContextPrincipal_nilChild_nonNil(t *testing.T) {
	t.Parallel()
	parent := httpauth.WithPrincipal(context.Background(), execview.PrincipalView{ID: "p"})
	out := auth.EnsureContextPrincipal(parent, nil)
	if out == nil {
		t.Fatal("expected non-nil context")
	}
	p, ok := httpauth.PrincipalFromContext(out)
	if !ok || p.ID != "p" {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
	out2 := auth.EnsureContextPrincipal(context.Background(), nil)
	if out2 == nil {
		t.Fatal("expected non-nil context")
	}
	if _, ok := httpauth.PrincipalFromContext(out2); ok {
		t.Fatalf("expected no principal")
	}
}

type stubProvider struct {
	res httpauth.AuthenticationResult
}

func (s stubProvider) Authenticate(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	_ = ctx
	return s.res, nil
}

type errProvider struct{}

func (errProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{}, context.DeadlineExceeded
}

type orderProvider struct {
	n     int
	order *[]int
	res   httpauth.AuthenticationResult
}

func (o orderProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	*o.order = append(*o.order, o.n)
	return o.res, nil
}
