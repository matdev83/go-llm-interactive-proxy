package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestStampBillingCallIDOncePerIncomingInvocation(t *testing.T) {
	t.Parallel()
	prep := &preparedRequest{
		aLeg:     b2bua.ALegRecord{ALegID: "a_shared"},
		baseline: lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess_shared"}},
	}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatal(err)
	}
	first := prep.billingCallID
	if err := first.Validate(); err != nil {
		t.Fatalf("stamped BillingCallID: %v", err)
	}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatal(err)
	}
	if prep.billingCallID != first {
		t.Fatal("retries and later stamps on the same incoming invocation must keep the original BillingCallID")
	}

	again := &preparedRequest{
		aLeg:     b2bua.ALegRecord{ALegID: "a_shared"},
		baseline: lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess_shared"}},
	}
	if err := stampBillingCallID(again); err != nil {
		t.Fatal(err)
	}
	if again.billingCallID == first {
		t.Fatal("a later call reusing the same A-leg/session must allocate a distinct BillingCallID")
	}
	firstKey, err := billing.NewCustomerOperationKey("acct", first)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := billing.NewCustomerOperationKey("acct", again.billingCallID)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey == secondKey {
		t.Fatal("reused A-leg/session must produce distinct customer billing operations")
	}
}

func TestStampBillingCallIDRemainsIndependentOfExposureIdentity(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.BillingIdentity = testBillingIdentity()
	prep := &preparedRequest{aLeg: b2bua.ALegRecord{ALegID: "a-1"}, baseline: lipapi.Call{ID: "call-1"}}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatal(err)
	}
	ex.stampExposureIdentity(context.Background(), prep, billing.CallExposure{
		AccountID: "acct", CallID: prep.billingCallID.String(),
		PricingRef:      billing.VersionRef{ID: "pricing:test", Version: "1"},
		ChargePolicyRef: billing.VersionRef{ID: "policy:test", Version: "1"},
	})
	if !prep.billingIdentityStamped || prep.billingAccountID != "acct" {
		t.Fatalf("exposure identity was not stamped: %+v", prep)
	}
	if string(prep.billingCallID) == prep.billingAccountID || string(prep.billingCallID) == "a-1" {
		t.Fatal("BillingCallID must not reuse A-leg or account identity")
	}
}

func TestFailoverAndParallelBLegsSharePreparedBillingCallID(t *testing.T) {
	t.Parallel()
	prep := &preparedRequest{
		aLeg:     b2bua.ALegRecord{ALegID: "a_shared"},
		baseline: lipapi.Call{Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
	}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("exec:m")
	if err != nil {
		t.Fatal(err)
	}
	plan := &routePlanState{sel: sel, budget: &attemptBudget{max: 3}}
	out := attemptOpenResult{
		opened: true,
		stream: lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}),
		cand:   routing.AttemptCandidate{Primary: routing.Primary{Backend: "exec", Model: "m"}},
	}
	got, err := TestExecutor().assembleExecutorStream(context.Background(), prep, plan, out)
	if err != nil {
		t.Fatal(err)
	}
	rs, ok := got.(*retryRecvStream)
	if !ok {
		t.Fatalf("want *retryRecvStream, got %T", got)
	}
	if rs.billingCallID != prep.billingCallID || rs.billingCallID == "" {
		t.Fatalf("assembled stream BillingCallID = %q, want prepared %q", rs.billingCallID, prep.billingCallID)
	}
	legs := []string{"b_fail", "b_retry", "b_par"}
	seen := make(map[billing.ProviderCostOperationKey]struct{}, len(legs))
	for _, bLeg := range legs {
		key, err := billing.NewProviderCostOperationKey(rs.billingCallID, bLeg)
		if err != nil {
			t.Fatal(err)
		}
		if key.CallID != prep.billingCallID {
			t.Fatalf("B-leg %s must share the incoming BillingCallID", bLeg)
		}
		seen[key] = struct{}{}
	}
	if len(seen) != len(legs) {
		t.Fatal("each B-leg must remain uniquely identified by BillingCallID + B-leg")
	}
}

func TestExecuteFailoverBLegsShareOneBillingCallID(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, lipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-shared", ContinuityKey: "sess-shared"},
		Route:   lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := billingCallIDFromStream(stream)
	if !ok {
		t.Fatal("Execute must stamp a request-local BillingCallID")
	}
	if err := id.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	aLegID := strings.TrimSpace(call.Session.ALegID)
	if aLegID == "" {
		t.Fatal("expected A-leg after execute")
	}
	atts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts = %d, want failover + winner", len(atts))
	}
	for _, att := range atts {
		key, err := billing.NewProviderCostOperationKey(id, att.BLegID)
		if err != nil {
			t.Fatal(err)
		}
		if key.CallID != id {
			t.Fatalf("B-leg %s must share BillingCallID %s", att.BLegID, id)
		}
	}
}

func TestExecuteReusesSessionButAllocatesDistinctBillingCallIDs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(3)
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	newCall := func() *lipapi.Call {
		return &lipapi.Call{
			Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-reuse", ContinuityKey: "sess-reuse"},
			Route:   lipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hi")},
			}},
		}
	}
	firstStream, err := ex.Execute(context.Background(), newCall())
	if err != nil {
		t.Fatal(err)
	}
	first, ok := billingCallIDFromStream(firstStream)
	if !ok {
		t.Fatal("first invocation missing BillingCallID")
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatal(err)
	}
	secondStream, err := ex.Execute(context.Background(), newCall())
	if err != nil {
		t.Fatal(err)
	}
	second, ok := billingCallIDFromStream(secondStream)
	if !ok {
		t.Fatal("second invocation missing BillingCallID")
	}
	if _, err := lipapi.Collect(context.Background(), secondStream); err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("subsequent calls on one session must produce distinct BillingCallIDs")
	}
	k1, err := billing.NewCustomerOperationKey("acct", first)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := billing.NewCustomerOperationKey("acct", second)
	if err != nil {
		t.Fatal(err)
	}
	if k1 == k2 {
		t.Fatal("distinct BillingCallIDs must produce distinct customer billing operations")
	}
	if opens.Load() != 2 {
		t.Fatalf("opens = %d, want 2", opens.Load())
	}
}

func TestExecuteResumeSameALegAllocatesDistinctBillingCallIDs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(5)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	firstCall := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-resume-aleg", ContinuityKey: "sess-resume-aleg"},
		Route:   lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("first")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := billingCallIDFromStream(firstStream)
	if !ok {
		t.Fatal("first invocation missing BillingCallID")
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatal(err)
	}
	firstALeg := strings.TrimSpace(firstCall.Session.ALegID)
	if firstALeg == "" {
		t.Fatal("expected A-leg after first execute")
	}
	if firstCall.Session.ResumeToken == "" || firstCall.Session.AuthoritativeSessionID == "" {
		t.Fatal("expected resume token and authoritative session for second call")
	}
	secondCall := &lipapi.Call{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: firstCall.Session.AuthoritativeSessionID,
			ContinuityKey:          firstCall.Session.ContinuityKey,
			ResumeToken:            firstCall.Session.ResumeToken,
			ALegID:                 firstALeg,
		},
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("second")},
		}},
	}
	secondStream, err := ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := billingCallIDFromStream(secondStream)
	if !ok {
		t.Fatal("second invocation missing BillingCallID")
	}
	if _, err := lipapi.Collect(context.Background(), secondStream); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(secondCall.Session.ALegID) != firstALeg {
		t.Fatalf("resume A-leg = %q, want same A-leg %q", secondCall.Session.ALegID, firstALeg)
	}
	if first == second {
		t.Fatal("later calls on the same resumed A-leg must produce distinct BillingCallIDs")
	}
	k1, err := billing.NewCustomerOperationKey("acct", first)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := billing.NewCustomerOperationKey("acct", second)
	if err != nil {
		t.Fatal(err)
	}
	if k1 == k2 {
		t.Fatal("same resumed A-leg must still produce distinct customer billing operations")
	}
}

func TestRequestLocalBillingCallIDIsNotOnProviderWireTypes(t *testing.T) {
	t.Parallel()
	assertNoBillingCallIDWireField(t, lipapi.Call{})
	assertNoBillingCallIDWireField(t, lipapi.SessionRef{})
	assertNoBillingCallIDWireField(t, backendplugin.FinalizeBillingRequest{})
	assertNoBillingCallIDWireField(t, routing.AttemptCandidate{})

	payload, err := json.Marshal(lipapi.Call{
		ID:      "call-1",
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-1", ALegID: "a-1"},
		Route:   lipapi.RouteIntent{Selector: "backend:model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(payload))
	if strings.Contains(lower, "billingcallid") || strings.Contains(lower, "billing_call_id") {
		t.Fatalf("lipapi.Call JSON must not carry BillingCallID: %s", payload)
	}
}

func billingCallIDFromStream(stream lipapi.EventStream) (billing.BillingCallID, bool) {
	rs, ok := stream.(*retryRecvStream)
	if !ok || rs == nil || rs.billingCallID == "" {
		return "", false
	}
	return rs.billingCallID, true
}

func assertNoBillingCallIDWireField(t *testing.T, sample any) {
	t.Helper()
	typ := reflect.TypeOf(sample)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s is not a struct", typ)
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		if name == "billingcallid" || strings.Contains(name, "billingcall") {
			t.Fatalf("%s.%s must not carry request-local BillingCallID on the provider wire", typ.Name(), field.Name)
		}
		tag := strings.ToLower(string(field.Tag))
		if strings.Contains(tag, "billing_call_id") || strings.Contains(tag, "billingcallid") {
			t.Fatalf("%s.%s tag leaks BillingCallID: %s", typ.Name(), field.Name, field.Tag)
		}
	}
}
