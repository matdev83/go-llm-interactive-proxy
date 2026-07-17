package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	coreauth "github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

type capturingContinueProvider struct {
	gotCtx context.Context
}

func (p *capturingContinueProvider) Authenticate(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	p.gotCtx = ctx
	return httpauth.AuthenticationResult{Type: httpauth.TypeContinue}, nil
}

func TestPeerIPFromRemoteAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		addr string
		want string
	}{
		{name: "ipv4_port", addr: "203.0.113.10:54321", want: "203.0.113.10"},
		{name: "ipv6_bracket_port", addr: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "host_only", addr: "203.0.113.11", want: "203.0.113.11"},
		{name: "ipv6_host_only", addr: "2001:db8::2", want: "2001:db8::2"},
		{name: "empty", addr: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := peerIPFromRemoteAddr(tc.addr); got != tc.want {
				t.Fatalf("peerIPFromRemoteAddr(%q)=%q want %q", tc.addr, got, tc.want)
			}
		})
	}
}

func TestPolicyProvider_allow_populatesIngressAttribution_peerIPFromRemoteAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		remoteAddr string
		wantPeer   string
	}{
		{name: "ipv4", remoteAddr: "198.51.100.7:9999", wantPeer: "198.51.100.7"},
		{name: "ipv6", remoteAddr: "[2001:db8::9]:8443", wantPeer: "2001:db8::9"},
		{name: "host_only", remoteAddr: "198.51.100.8", wantPeer: "198.51.100.8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubCoreAuthenticator{dec: auth.Decision{
				Outcome:   auth.OutcomeAllow,
				Principal: execview.PrincipalView{ID: "u1"},
				Device:    auth.DeviceIdentity{ID: "dev-1", KeyID: "key-1", Fingerprint: "fp-1"},
			}}
			p := NewPolicyProvider(stub, nil, PolicySnapshot{
				AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
			}, nil)
			req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", "203.0.113.50")
			req.Header.Set("X-Real-IP", "203.0.113.51")
			req.Header.Set("Forwarded", "for=203.0.113.52")
			res, err := p.Authenticate(req.Context(), httptest.NewRecorder(), req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Type != httpauth.TypePrincipal {
				t.Fatalf("type: %v", res.Type)
			}
			if res.IngressAttribution.PeerIP != tc.wantPeer {
				t.Fatalf("PeerIP: got %q want %q", res.IngressAttribution.PeerIP, tc.wantPeer)
			}
			if res.IngressAttribution.FrontendID != "openai_compatible" {
				t.Fatalf("FrontendID: %q", res.IngressAttribution.FrontendID)
			}
			if res.IngressAttribution.DeviceID != "dev-1" || res.IngressAttribution.KeyID != "key-1" || res.IngressAttribution.Fingerprint != "fp-1" {
				t.Fatalf("device fields: %+v", res.IngressAttribution)
			}
		})
	}
}

func TestPolicyProvider_allow_doesNotAttachMatcherDuringAuthenticate(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u1"},
		Device:    auth.DeviceIdentity{KeyID: "app-key"},
	}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := p.Authenticate(req.Context(), httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypePrincipal {
		t.Fatalf("type: %v", res.Type)
	}
	if gotM, ok := httpauth.CredentialMatcherFromContext(req.Context()); ok || gotM != nil {
		t.Fatalf("authenticate must not attach matcher, got ok=%v matcher=%v", ok, gotM)
	}
}

func TestPolicyProvider_allow_noMatcherWithoutPresentedCredential(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u1"},
	}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerLocalNoop, RequiredLevel: auth.LevelNone,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:2"
	res, err := p.Authenticate(req.Context(), httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypePrincipal {
		t.Fatalf("type: %v", res.Type)
	}
	if gotM, ok := httpauth.CredentialMatcherFromContext(req.Context()); ok || gotM != nil {
		t.Fatalf("allow without presented credential must not attach matcher, got ok=%v matcher=%v", ok, gotM)
	}
}

func TestPolicyProvider_nonAllowDoesNotAttachMatcher(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dec  auth.Decision
		err  error
	}{
		{
			name: "deny",
			dec: auth.Decision{
				Outcome:   auth.OutcomeDeny,
				Principal: execview.PrincipalView{ID: "u1"},
			},
		},
		{
			name: "challenge",
			dec: auth.Decision{
				Outcome:   auth.OutcomeChallenge,
				Principal: execview.PrincipalView{ID: "u1"},
			},
		},
		{
			name: "error",
			err:  errors.New("auth backend unavailable"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubCoreAuthenticator{dec: tc.dec, err: tc.err}
			p := NewPolicyProvider(stub, nil, PolicySnapshot{
				AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
			}, nil)
			req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
			req.Header.Set("Authorization", "Bearer "+testkit.SyntheticBearerCredential)
			_, _ = p.Authenticate(req.Context(), httptest.NewRecorder(), req)
			if gotM, ok := httpauth.CredentialMatcherFromContext(req.Context()); ok || gotM != nil {
				t.Fatalf("non-allow path must not attach matcher, got ok=%v matcher=%v", ok, gotM)
			}
		})
	}
}

type attributionStubProvider struct {
	res httpauth.AuthenticationResult
}

func (s attributionStubProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return s.res, nil
}

func TestMiddleware_attachesAttributionAndMatcher(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	attr := httpauth.IngressAttribution{PeerIP: "203.0.113.1", FrontendID: "gemini", KeyID: "kid"}
	authProvider := NewPolicyProvider(&stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u"},
		Device:    auth.DeviceIdentity{KeyID: "kid"},
	}}, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	capture := &capturingContinueProvider{}
	var gotCtx context.Context
	h := Middleware(nil, []httpauth.Provider{authProvider, capture}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	req.RemoteAddr = "203.0.113.1:1234"
	req.Header.Set("Authorization", "Bearer "+secret)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	gotAttr, ok := httpauth.IngressAttributionFromContext(gotCtx)
	if !ok {
		t.Fatal("expected attribution in final context")
	}
	if gotAttr.PeerIP != attr.PeerIP || gotAttr.FrontendID != "gemini" || gotAttr.KeyID != attr.KeyID {
		t.Fatalf("attribution got=%+v want=%+v", gotAttr, attr)
	}
	gotM, ok := httpauth.CredentialMatcherFromContext(gotCtx)
	if !ok || gotM == nil {
		t.Fatalf("matcher ok=%v got=%v", ok, gotM)
	}
	var resolver secretguard.MatcherResolver = secretguard.ContextMatcherResolver{}
	resolved, err := resolver.Resolve(gotCtx)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil {
		t.Fatal("ContextMatcherResolver must resolve middleware-attached matcher")
	}
	if capture.gotCtx == nil {
		t.Fatal("expected second provider to observe accumulated context")
	}
	if p, ok := httpauth.PrincipalFromContext(capture.gotCtx); !ok || p.ID != "u" {
		t.Fatalf("capture principal ok=%v got=%+v", ok, p)
	}
	if m, ok := httpauth.CredentialMatcherFromContext(capture.gotCtx); !ok || m == nil {
		t.Fatalf("capture matcher ok=%v got=%v", ok, m)
	}
}

func TestContextMatcherResolver_afterMiddleware_noEnvFallbackWhenAbsent(t *testing.T) {
	// Not parallel: uses t.Setenv.
	const probe = "STDHTTP_AUTH_MATCHER_ABSENT_PROBE"
	t.Setenv(probe, "must-not-be-used-as-credential")
	if _, ok := os.LookupEnv(probe); !ok {
		t.Fatal("setup: probe env not set")
	}
	p := attributionStubProvider{res: httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "u"},
	}}
	var gotCtx context.Context
	h := Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	var resolver secretguard.MatcherResolver = secretguard.ContextMatcherResolver{}
	got, err := resolver.Resolve(gotCtx)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("absent matcher must resolve to nil without env fallback")
	}
}

func TestMiddleware_customProviderDoesNotInventMatcher(t *testing.T) {
	t.Parallel()
	p := attributionStubProvider{res: httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "custom"},
	}}
	var gotCtx context.Context
	h := Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if gotM, ok := httpauth.CredentialMatcherFromContext(gotCtx); ok || gotM != nil {
		t.Fatalf("custom provider must not invent matcher, got ok=%v matcher=%v", ok, gotM)
	}
}

func TestExactCredentialMatcher_scanAndRedactBytes(t *testing.T) {
	t.Parallel()
	secret := []byte(testkit.SyntheticUnicodeSecret)
	m := newExactCredentialMatcher(string(secret), "")
	input := append([]byte("x"), append(secret, []byte("y")...)...)
	findings, err := m.ScanBytes(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SecretRefName != "request_credential" {
		t.Fatalf("findings: %+v", findings)
	}
	if findings[0].OccurrenceCount != 1 {
		t.Fatalf("count: %d", findings[0].OccurrenceCount)
	}
	out, findings, err := m.RedactBytes(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("redact findings: %+v", findings)
	}
	if bytes.Contains(out, secret) {
		t.Fatal("redacted output still contains secret bytes")
	}
	if len(out) != len(input) {
		t.Fatalf("length: got %d want %d", len(out), len(input))
	}
	want := append([]byte("x"), append(bytes.Repeat([]byte("*"), len(secret)), []byte("y")...)...)
	if !bytes.Equal(out, want) {
		t.Fatalf("redacted mismatch")
	}
}

func TestPolicyProvider_allow_matcherUsesDefaultRefWhenKeyIDEmpty(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u1"},
	}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/foo", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	var gotM secretguard.Matcher
	var sawInner bool
	Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawInner = true
		var ok bool
		gotM, ok = httpauth.CredentialMatcherFromContext(r.Context())
		if !ok || gotM == nil {
			t.Fatal("expected matcher in middleware context")
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
	if !sawInner {
		t.Fatal("expected middleware to reach inner handler")
	}
	findings, err := gotM.ScanString(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SecretRefName != "request_credential" {
		t.Fatalf("findings: %+v", findings)
	}
}

// Ensure LocalAPIKeyAuthenticator path still builds matcher from request bearer (adapter),
// and Decision remains free of raw secret accessors.
func TestPolicyProvider_localAPIKeyAllow_matcherFromRequestNotDecision(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	ak, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "k-local", PrincipalID: "p1", Key: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewPolicyProvider(ak, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/chat", nil)
	req.RemoteAddr = "10.0.0.1:80"
	req.Header.Set("Authorization", "Bearer "+secret)
	var gotM secretguard.Matcher
	var gotAttr httpauth.IngressAttribution
	var sawInner bool
	Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawInner = true
		gotAttr, _ = httpauth.IngressAttributionFromContext(r.Context())
		var ok bool
		gotM, ok = httpauth.CredentialMatcherFromContext(r.Context())
		if !ok || gotM == nil {
			t.Fatal("expected matcher built from request bearer")
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
	if !sawInner {
		t.Fatal("expected middleware to reach inner handler")
	}
	if gotAttr.KeyID != "k-local" {
		t.Fatalf("KeyID: %q", gotAttr.KeyID)
	}
	findings, err := gotM.ScanString(context.Background(), "body "+secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SecretRefName != "k-local" {
		t.Fatalf("findings: %+v", findings)
	}
}
