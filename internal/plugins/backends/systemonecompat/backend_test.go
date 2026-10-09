package systemonecompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func backendNode(t *testing.T, base string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(fmt.Sprintf("backend_prefix: decision\nbase_url: %s\nmodels:\n  source: inline\n  items:\n    - canonical_id: jev\n      native_id: jev\n", base)), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func decisionCall() lipapi.Call {
	r := wireDecision()
	return lipapi.Call{Decision: &r, Invocation: lipapi.Invocation{Operation: lipapi.OperationDecisionEvaluate, DeliveryMode: lipapi.DeliveryModeNonStreaming}}
}

func TestBackend_StatusClassificationAndPrivacy(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 413, 422, 401, 402, 403, 404, 429, 500, 503, 524, 529} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"detail":[{"loc":["body","questions"],"msg":"evidence-private","type":"value_error","input":"evidence-private","ctx":{"echo":"evidence-private"}}]}`)
			}))
			defer srv.Close()
			be, err := BuildCompatible("decision", backendNode(t, srv.URL), srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			stream, err := be.Open(t.Context(), decisionCall(), routing.AttemptCandidate{})
			terminal := status == 400 || status == 413 || status == 422
			if stream != nil || err == nil || lipapi.IsDecisionReject(err) != terminal || lipapi.IsRecoverablePreOutput(err) == terminal {
				t.Fatalf("status %d: stream=%v error=%v", status, stream, err)
			}
			if strings.Contains(err.Error(), "evidence-private") {
				t.Fatal("upstream echoed content crossed error boundary")
			}
			if terminal {
				rejection, ok := errors.AsType[*lipapi.DecisionRejectError](err)
				if !ok || rejection.Field != "body.questions" {
					t.Fatalf("rejection field = %+v", rejection)
				}
			}
		})
	}
}

func TestBackend_RejectsChatAndStreamingBeforeHTTP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, wireResponse)
	}))
	defer srv.Close()
	be, err := BuildCompatible("decision", backendNode(t, srv.URL), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, decisions := be.Caps[lipapi.CapabilityDecisions]
	if len(be.Caps) != 1 || !decisions || be.EnforcesMaxOutputTokens {
		t.Fatalf("backend capabilities = %+v", be)
	}
	for _, call := range []lipapi.Call{
		{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}},
		func() lipapi.Call {
			c := decisionCall()
			c.Invocation.TransportMode = lipapi.TransportModeStreaming
			return c
		}(),
	} {
		stream, err := be.Open(t.Context(), call, routing.AttemptCandidate{})
		if stream != nil || !lipapi.IsReject(err) {
			t.Fatalf("unsupported call: stream=%v err=%v", stream, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported operation reached upstream")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = be.Open(ctx, decisionCall(), routing.AttemptCandidate{})
	if !errors.Is(err, context.Canceled) || lipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestBackend_TransportTimeoutRecoverableButCallerCancellationTerminal(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{context.DeadlineExceeded, errors.New("transport evidence-private")} {
		err := classifyError(t.Context(), cause)
		if !lipapi.IsRecoverablePreOutput(err) || strings.Contains(err.Error(), "evidence-private") {
			t.Fatalf("transport classification = %v", err)
		}
	}
	err := classifyError(t.Context(), context.Canceled)
	if !errors.Is(err, context.Canceled) || lipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("cancellation classification = %v", err)
	}
}
