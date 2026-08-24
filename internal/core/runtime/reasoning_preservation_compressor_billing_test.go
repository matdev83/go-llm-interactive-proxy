package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type reasoningCompressorCapture struct {
	mu    sync.Mutex
	calls []billing.CallUsageRecord
	legs  []billing.CallLegUsageRecord
	opens []reasoningCompressorOpen
}

type reasoningCompressorOpen struct {
	call      lipapi.Call
	auxiliary bool
	scope     scope.PrincipalScopeView
	detached  bool
	role      string
}

func (c *reasoningCompressorCapture) appendCall(_ context.Context, r billing.CallUsageRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, r)
	return nil
}

func (c *reasoningCompressorCapture) appendLeg(_ context.Context, r billing.CallLegUsageRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.legs = append(c.legs, r)
	return nil
}

func (c *reasoningCompressorCapture) snapshot() (calls []billing.CallUsageRecord, legs []billing.CallLegUsageRecord, opens []reasoningCompressorOpen) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]billing.CallUsageRecord(nil), c.calls...), append([]billing.CallLegUsageRecord(nil), c.legs...), append([]reasoningCompressorOpen(nil), c.opens...)
}

func newReasoningCompressorExecutor(t *testing.T, capture *reasoningCompressorCapture, accountID string) *Executor {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.SecureSession = testSecureManager(t, memory.New(memory.Options{SimulateDurable: true}), store)
	ex.SyntheticLocalPrincipal = true
	ex.MaxAttempts = 2
	ex.Now = func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }
	ex.BillingIdentity = BillingIdentity{
		AccountID: func(ctx context.Context, _ lipapi.Call) string {
			got, ok := scope.ScopeFromContext(ctx)
			if !ok {
				return ""
			}
			return "acct:" + got.PrincipalID.String()
		},
		CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "pricing:reasoning-compression", Version: "1"}
		},
		ChargePolicyRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "policy:reasoning-compression", Version: "1"}
		},
		OperatorRateRef: func(context.Context, string, string) billing.VersionRef {
			return billing.VersionRef{ID: "operator:reasoning-compression", Version: "1"}
		},
	}
	ex.BillingCreditGate = creditGateFunc(func(_ context.Context, got string) error {
		if got != accountID {
			return errors.New("unexpected account at credit screen: " + got)
		}
		return nil
	})
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		if in.CallID == "" || in.ALegID == "" {
			return billing.CallExposure{}, errors.New("missing exposure identity")
		}
		return billing.CallExposure{
			AccountID:       accountID,
			CallID:          in.CallID,
			PricingRef:      billing.VersionRef{ID: "pricing:reasoning-compression", Version: "1"},
			ChargePolicyRef: billing.VersionRef{ID: "policy:reasoning-compression", Version: "1"},
			Status:          billing.ExposureOpen,
		}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{appendCall: capture.appendCall, appendLeg: capture.appendLeg}
	return ex
}

func TestReasoningPreservationCompressor_BillingAttribution_ScopeAndLegIsolation(t *testing.T) {
	principalID := "principal-reasoning-compressor"
	accountID := "acct:" + principalID
	capture := &reasoningCompressorCapture{}
	ex := newReasoningCompressorExecutor(t, capture, accountID)
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, candidate routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				gotScope, _ := scope.ScopeFromContext(ctx)
				isAux := gotScope.Origin == scope.OriginInternal
				role := ""
				if raw, ok := call.Extensions["lip.aux.lineage.v1"]; ok {
					var ln struct {
						Role string `json:"role"`
					}
					_ = json.Unmarshal(raw, &ln)
					role = ln.Role
				}
				capture.mu.Lock()
				capture.opens = append(capture.opens, reasoningCompressorOpen{call: lipapi.CloneCall(call), auxiliary: isAux, scope: gotScope, detached: isAux, role: role})
				capture.mu.Unlock()
				input, output, total, cost, dedupe := 11, 5, 16, int64(101), "primary-provider-charge"
				if isAux {
					input, output, total, cost, dedupe = 3, 2, 5, int64(37), "compressor-provider-charge"
				}
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: strings.Repeat("x", output)},
					{
						Kind: lipapi.EventUsageDelta, InputTokens: input, OutputTokens: output, TotalTokens: total,
						CostNanoUnits: cost, Currency: "USD", CostPresent: true,
						UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
						Accounting: lipapi.UsageAccountingMetadata{
							Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
							Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: dedupe,
						},
					},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	// primary client scope
	rootCtx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(principalID),
		Origin:      scope.OriginClient,
	})
	primaryCall := &lipapi.Call{
		Session:  lipapi.SessionRef{ClientSessionID: "client-reasoning", ContinuityKey: "branch-primary"},
		Route:    lipapi.RouteIntent{Selector: "backend:primary-model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("primary request")}}},
	}
	primaryStream, err := ex.Execute(rootCtx, primaryCall)
	if err != nil {
		t.Fatalf("primary Execute: %v", err)
	}
	primary, err := lipapi.Collect(rootCtx, primaryStream)
	if err != nil {
		t.Fatalf("primary collect: %v", err)
	}

	// build compressor auxiliary request manually (mirrors reasoningpreservation.BuildCompressorAuxRequest)
	compressorReq := auxiliary.Request{
		Role:          "reasoning_preservation_compressor",
		Visibility:    "private",
		SessionMode:   auxiliary.SessionModeDetached,
		ParentTraceID: "trace-reasoning-primary",
		ParentALegID:  primaryCall.Session.ALegID,
		Call: &lipapi.Call{
			Route: lipapi.RouteIntent{Selector: "backend:reasoning-compressor-model"},
			Messages: []lipapi.Message{
				{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("You are the Go-LIP reasoning semantic compressor.")}},
				{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(`{"schema_version":1,"segments":[{"index":0,"text":"sanitized reasoning"}]}`)}},
			},
			ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone},
		},
		DisablePlugins: []string{"reasoning-output-preservation"},
	}

	// ensure control-plane metadata not in model prompt
	var promptBlob strings.Builder
	for _, m := range compressorReq.Call.Messages {
		for _, p := range m.Parts {
			promptBlob.WriteString(p.Text)
		}
	}
	for _, leak := range []string{principalID, accountID, "trace-reasoning-primary"} {
		if strings.Contains(promptBlob.String(), leak) {
			t.Fatalf("control-plane %q leaked into compressor prompt: %q", leak, promptBlob.String())
		}
	}
	if !strings.Contains(promptBlob.String(), "sanitized reasoning") {
		t.Fatalf("sanitized reasoning missing from prompt: %q", promptBlob.String())
	}

	auxClient := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex })
	childStream, err := auxClient.Stream(rootCtx, compressorReq)
	if err != nil {
		t.Fatalf("compressor Stream: %v", err)
	}
	child, err := lipapi.Collect(rootCtx, childStream)
	if err != nil {
		t.Fatalf("child collect: %v", err)
	}

	if primary.InputTokens != 11 || primary.OutputTokens != 5 {
		t.Fatalf("primary usage changed by compressor: %+v", primary)
	}
	if child.InputTokens != 3 || child.OutputTokens != 2 {
		t.Fatalf("compressor usage: %+v", child)
	}

	calls, legs, opens := capture.snapshot()
	if len(calls) != 2 {
		t.Fatalf("call closures=%d want 2 (primary+compressor)", len(calls))
	}
	if len(opens) < 2 {
		t.Fatalf("opens=%d want >=2", len(opens))
	}
	var primaryALeg, childALeg string
	var primaryCallID, childCallID billing.BillingCallID
	var sawAuxRole bool
	for _, o := range opens {
		if o.auxiliary {
			childALeg = o.call.Session.ALegID
			if o.scope.PrincipalID.String() != principalID || o.scope.Origin != scope.OriginInternal {
				t.Fatalf("auxiliary scope not propagated correctly: %+v", o.scope)
			}
			if o.role != "reasoning_preservation_compressor" {
				t.Fatalf("auxiliary lineage role=%q want reasoning_preservation_compressor", o.role)
			}
			sawAuxRole = true
		} else {
			primaryALeg = o.call.Session.ALegID
		}
	}
	if !sawAuxRole {
		t.Fatalf("no auxiliary open with compressor role observed")
	}
	if primaryALeg == "" || childALeg == "" || primaryALeg == childALeg {
		t.Fatalf("A-leg distinctness failed primary=%q child=%q", primaryALeg, childALeg)
	}
	for _, c := range calls {
		if c.AccountID != accountID {
			t.Fatalf("call account %q want %q", c.AccountID, accountID)
		}
		if c.Workload.Class == billing.WorkloadClassAuxiliary {
			if c.Workload.Role != billing.WorkloadRoleReasoningPreservationCompressor {
				t.Fatalf("auxiliary workload role %q want %q", c.Workload.Role, billing.WorkloadRoleReasoningPreservationCompressor)
			}
			childCallID = c.CallID
		} else {
			primaryCallID = c.CallID
			if !c.Workload.IsZero() && c.Workload.Class != billing.WorkloadClassPrimary {
				t.Fatalf("primary call workload unexpected: %+v", c.Workload)
			}
		}
	}
	if primaryCallID == "" || childCallID == "" || primaryCallID == childCallID {
		t.Fatalf("BillingCallID distinctness failed primary=%q child=%q", primaryCallID, childCallID)
	}
	// leg attribution
	var primaryLegs, childLegs []billing.CallLegUsageRecord
	for _, leg := range legs {
		if leg.Workload.Class == billing.WorkloadClassAuxiliary && leg.Workload.Role != billing.WorkloadRoleReasoningPreservationCompressor {
			t.Fatalf("aux leg workload role %q", leg.Workload.Role)
		}
		switch leg.CallID {
		case primaryCallID:
			primaryLegs = append(primaryLegs, leg)
		case childCallID:
			childLegs = append(childLegs, leg)
		default:
			t.Fatalf("leg with unknown BillingCallID %q", leg.CallID)
		}
		if leg.AttemptSeq <= 0 {
			t.Fatalf("leg AttemptSeq must be positive: %+v", leg)
		}
		if strings.TrimSpace(leg.Evidence.DedupeKey) == "" || leg.Evidence.Source == billing.EvidenceSourceUnknown {
			t.Fatalf("leg missing evidence identity: %+v", leg)
		}
	}
	if len(primaryLegs) != 1 || len(childLegs) != 1 {
		t.Fatalf("leg counts primary=%d child=%d want 1 each", len(primaryLegs), len(childLegs))
	}
	if childLegs[0].Evidence.OutputTokens.Value != 2 || childLegs[0].Evidence.Cost.NanoUnits != 37 {
		t.Fatalf("compressor evidence not preserved: %+v", childLegs[0].Evidence)
	}
	// primary protocol usage excludes compressor; account/operator aggregate includes
	aggregateOutput := int64(0)
	for _, leg := range legs {
		if leg.Evidence.OutputTokens.Present {
			aggregateOutput += leg.Evidence.OutputTokens.Value
		}
	}
	if aggregateOutput != int64(primary.OutputTokens+child.OutputTokens) {
		t.Fatalf("aggregate output %d want primary+compressor %d", aggregateOutput, primary.OutputTokens+child.OutputTokens)
	}
}

func TestReasoningPreservationCompressor_BillingAdmission_PreSubmitRejectionNoProviderWork(t *testing.T) {
	principalID := "principal-admission-reject"
	accountID := "acct:" + principalID
	capture := &reasoningCompressorCapture{}
	ex := newReasoningCompressorExecutor(t, capture, accountID)
	// deny compressor admission at cheap credit screen
	ex.BillingCreditGate = creditGateFunc(func(_ context.Context, got string) error {
		return billing.ErrCreditScreenDenied
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				t.Fatalf("provider must not be opened on pre-submit admission rejection")
				return nil, nil
			},
		},
	}
	rootCtx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		PrincipalID: scope.Known(principalID),
		Origin:      scope.OriginClient,
	})
	req := auxiliary.Request{
		Role:        "reasoning_preservation_compressor",
		Visibility:  "private",
		SessionMode: auxiliary.SessionModeDetached,
		Call: &lipapi.Call{
			Route:    lipapi.RouteIntent{Selector: "backend:reasoning-compressor-model"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("text")}}},
		},
	}
	_, err := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex }).Stream(rootCtx, req)
	if !errors.Is(err, ErrBillingAdmissionDenied) {
		t.Fatalf("expected billing admission denied, got %v", err)
	}
	if !errors.Is(err, ErrBillingCreditScreenDenied) {
		t.Fatalf("expected credit screen denied classification, got %v", err)
	}
	calls, legs, _ := capture.snapshot()
	if len(calls) != 0 || len(legs) != 0 {
		t.Fatalf("pre-submit rejection must not produce billing records calls=%d legs=%d", len(calls), len(legs))
	}
}

func TestReasoningPreservationCompressor_BillingAccountability_SubmittedStillBillable(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{name: "invalid json", output: "not json"},
		{name: "raw oversize", output: strings.Repeat("x", 1<<20)},
		{name: "stale digest surrogate", output: `{"schema_version":1,"segments":[{"index":0,"text":"stale"}]}`},
		{name: "insufficient savings", output: `{"schema_version":1,"segments":[{"index":0,"text":"almost same length as source text that is very long and not compressed enough"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			safeName := strings.ReplaceAll(strings.ReplaceAll(tc.name, " ", "_"), "/", "_")
			principalID := "principal-submitted-billable-" + safeName
			accountID := "acct:" + principalID
			capture := &reasoningCompressorCapture{}
			ex := newReasoningCompressorExecutor(t, capture, accountID)
			ex.Backends = map[string]execbackend.Backend{
				"backend": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(ctx context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						// compressor output text returned as model output, but usage still billable
						return lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventResponseStarted},
							{Kind: lipapi.EventMessageStarted},
							{Kind: lipapi.EventTextDelta, Delta: tc.output},
							{
								Kind: lipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 10, TotalTokens: 14,
								CostNanoUnits: 55, Currency: "USD", CostPresent: true,
								UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
								Accounting: lipapi.UsageAccountingMetadata{
									Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
									Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "compressor-charge-" + safeName,
								},
							},
							{Kind: lipapi.EventResponseFinished},
						}), nil
					},
				},
			}
			rootCtx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
				PrincipalID: scope.Known(principalID),
				Origin:      scope.OriginClient,
			})
			req := auxiliary.Request{
				Role:        "reasoning_preservation_compressor",
				Visibility:  "private",
				SessionMode: auxiliary.SessionModeDetached,
				Call: &lipapi.Call{
					Route:    lipapi.RouteIntent{Selector: "backend:reasoning-compressor-model"},
					Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("source reasoning text for compression")}}},
				},
			}
			stream, err := auxreq.NewClient(func() auxreq.ExecutorRunner { return ex }).Stream(rootCtx, req)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			collected, err := lipapi.Collect(rootCtx, stream)
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			// collected text may be invalid / oversize / stale / insufficient but still collected
			_ = collected
			calls, legs, _ := capture.snapshot()
			if len(calls) != 1 || len(legs) != 1 {
				t.Fatalf("submitted invalid compressor must still produce billing call=%d legs=%d", len(calls), len(legs))
			}
			if legs[0].Evidence.Cost.NanoUnits != 55 || legs[0].Evidence.OutputTokens.Value != 10 {
				t.Fatalf("billable evidence not preserved for %q: %+v", tc.name, legs[0].Evidence)
			}
			if legs[0].Workload.Role != billing.WorkloadRoleReasoningPreservationCompressor {
				t.Fatalf("workload role lost for billable invalid result: %+v", legs[0].Workload)
			}
		})
	}
}

func TestReasoningPreservationCompressor_BackgroundScheduler_WorkloadAndScope(t *testing.T) {
	principalID := "principal-bg-scheduler"
	accountID := "acct:" + principalID
	capture := &reasoningCompressorCapture{}
	ex := newReasoningCompressorExecutor(t, capture, accountID)
	var bgOpens int
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				bgOpens++
				sc, _ := scope.ScopeFromContext(ctx)
				if sc.PrincipalID.String() != principalID {
					t.Errorf("bg scope principal %q want %q", sc.PrincipalID.String(), principalID)
				}
				if sc.Origin != scope.OriginInternal {
					t.Errorf("bg scope origin %q want internal", sc.Origin)
				}
				// verify detached role is reasoning_preservation_compressor
				if raw, ok := call.Extensions["lip.aux.lineage.v1"]; ok {
					var ln struct {
						Role string `json:"role"`
					}
					_ = json.Unmarshal(raw, &ln)
					if ln.Role != "reasoning_preservation_compressor" {
						t.Errorf("bg lineage role %q", ln.Role)
					}
				}
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{
						Kind: lipapi.EventUsageDelta, OutputTokens: 2, TotalTokens: 2,
						CostNanoUnits: 10, Currency: "USD", CostPresent: true,
						UsagePresence: lipapi.UsagePresence{OutputTokens: true, TotalTokens: true},
						Accounting: lipapi.UsageAccountingMetadata{
							Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
							Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "bg-compressor-charge",
						},
					},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	sched, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return ex }, auxreq.SchedulerConfig{
		Workers: 1, QueueCapacity: 4, MaxResults: 4, JobTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sched.Close() }()

	rootCtx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		PrincipalID: scope.Known(principalID),
		Origin:      scope.OriginClient,
	})
	req := auxiliary.Request{
		Role:          "reasoning_preservation_compressor",
		Visibility:    "private",
		SessionMode:   auxiliary.SessionModeDetached,
		ParentTraceID: "trace-bg",
		Call: &lipapi.Call{
			Route:    lipapi.RouteIntent{Selector: "backend:reasoning-compressor-model"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("bg reasoning text")}}},
		},
	}
	jobID, err := sched.SubmitCollect(rootCtx, req, auxiliary.SubmitOptions{CoalesceKey: "bg-reasoning-compressor-key"})
	if err != nil {
		t.Fatalf("SubmitCollect: %v", err)
	}
	collected, err := sched.Await(context.Background(), jobID)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if collected.OutputTokens != 2 {
		t.Fatalf("collected output %d want 2", collected.OutputTokens)
	}
	if bgOpens != 1 {
		t.Fatalf("bg opens %d want 1", bgOpens)
	}
	calls, legs, _ := capture.snapshot()
	if len(calls) != 1 || len(legs) != 1 {
		t.Fatalf("bg billing calls %d legs %d want 1 each", len(calls), len(legs))
	}
	if legs[0].Workload.Class != billing.WorkloadClassAuxiliary || legs[0].Workload.Role != billing.WorkloadRoleReasoningPreservationCompressor {
		t.Fatalf("bg workload %+v", legs[0].Workload)
	}
	if legs[0].CallID == "" || legs[0].AttemptSeq <= 0 {
		t.Fatalf("bg leg missing BillingCallID or AttemptSeq: %+v", legs[0])
	}
}
