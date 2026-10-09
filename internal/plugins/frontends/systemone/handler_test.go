package systemone

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	stdhttpauth "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
	frontendcontract "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/frontend"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

func TestHandler_RealAuthenticationAndDecodeErrorsBeforeExecution(t *testing.T) {
	t.Parallel()
	exec := &frontendcontract.CapturingExecutor{Script: frontendcontract.EventScript{Events: []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventDecisionResult, Decision: &lipapi.DecisionResult{Model: "jev", Answers: []lipapi.DecisionAnswer{{QuestionID: "q", Kind: lipapi.DecisionKindNoul, PTrue: 0.8}}}},
		{Kind: lipapi.EventResponseFinished},
	}}}
	mux := http.NewServeMux()
	var config yaml.Node
	if err := yaml.Unmarshal([]byte("max_questions: 1"), &config); err != nil {
		t.Fatal(err)
	}
	if err := Mount(mux, lipsdk.FrontendMountOptions{Exec: exec, PluginCfg: config, RoutePrefixes: []string{"decision"}, MaxRequestBodyBytes: 2048}); err != nil {
		t.Fatal(err)
	}
	auth, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{{KeyID: "client", PrincipalID: "user", Key: "test-client-key-for-systemone"}})
	if err != nil {
		t.Fatal(err)
	}
	provider := stdhttpauth.NewPolicyProvider(auth, nil, stdhttpauth.PolicySnapshot{HandlerKind: sdkauth.HandlerLocalAPIKey, RequiredLevel: sdkauth.LevelAPIKey}, nil)
	handler := stdhttpauth.Middleware(nil, []httpauth.Provider{provider}, mux)
	body := `{"model":"decision:jev","state":"x","questions":{"q":{"type":"noul"}}}`
	request := func(body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		r.Header.Set(routeselect.HeaderRouteSelector, "must-not-override:bad")
		r.Header.Set("X-Upstream-Session", "must-not-forward")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(body, ""); w.Code != http.StatusUnauthorized || len(exec.Calls) != 0 {
		t.Fatalf("unauthenticated request = %d, calls=%d", w.Code, len(exec.Calls))
	}
	if w := request(body, "test-client-key-for-systemone"); w.Code != http.StatusOK || len(exec.Calls) != 1 {
		t.Fatalf("authenticated request = %d %s, calls=%d", w.Code, w.Body.String(), len(exec.Calls))
	}
	call := exec.Calls[0]
	if call.Route.Selector != "decision:jev" || call.Invocation.ClientUserAgent != "" || len(call.Extensions) != 0 {
		t.Fatalf("client controls leaked into decision call: %+v", call)
	}
	for body, field := range map[string]string{
		`{"model":"decision:jev","state":"x","questions":{"q":{"type":"noul"}},"unknown":true}`:      "unknown",
		`{"model":"decision:jev","state":"x","questions":{"q":{"type":"noul"},"r":{"type":"noul"}}}`: "questions",
	} {
		w := request(body, "test-client-key-for-systemone")
		var envelope struct {
			Detail []struct {
				Loc []string `json:"loc"`
			} `json:"detail"`
		}
		if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || w.Code != 422 || len(envelope.Detail) != 1 || len(envelope.Detail[0].Loc) != 2 || envelope.Detail[0].Loc[1] != field || len(exec.Calls) != 1 {
			t.Fatalf("invalid field request = %d %s, calls=%d", w.Code, w.Body.String(), len(exec.Calls))
		}
	}
	if w := request(strings.Repeat(" ", 2049), "test-client-key-for-systemone"); w.Code != 413 || len(exec.Calls) != 1 {
		t.Fatalf("oversized request = %d, calls=%d", w.Code, len(exec.Calls))
	}
}
