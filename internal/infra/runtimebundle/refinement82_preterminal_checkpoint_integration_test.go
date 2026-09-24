package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	_ "modernc.org/sqlite"
)

// refinement82GatedUsageStream delivers prefix events, then blocks until the
// test releases the gate, then delivers the terminal suffix. The provider V2
// usage observation is pre-loaded into the ProviderEvidenceBuffer exactly
// like the production adapter seam, so the runtime drains it through
// drainSidebandEvidence while the B-leg is still live.
type refinement82GatedUsageStream struct {
	*coremetering.ProviderEvidenceBuffer
	accountID string
	prefix    []lipapi.Event
	suffix    []lipapi.Event
	release   chan struct{}
	pos       atomic.Int32
	suffixPos atomic.Int32
}

func newRefinement82GatedUsageStream(accountID string, release chan struct{}) *refinement82GatedUsageStream {
	stream := &refinement82GatedUsageStream{
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
		accountID:              accountID,
		prefix: []lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: "partial"},
			// Terminal V1 token evidence WITHOUT accounting identity: it stays
			// a V1 fallback (never a V2 provider observation) and makes the
			// terminal call rateable for default retail. The preterminal V2
			// checkpoint above remains the only provider-charge observation.
			{
				Kind:          lipapi.EventUsageDelta,
				InputTokens:   billingHostLoopInputTokens,
				OutputTokens:  billingHostLoopOutputTokens,
				TotalTokens:   billingHostLoopInputTokens + billingHostLoopOutputTokens,
				UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
			},
		},
		suffix:  []lipapi.Event{{Kind: lipapi.EventResponseFinished, FinishReason: "stop"}},
		release: release,
	}
	stream.AddUsageEvent(lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		CostNanoUnits: refinement82OperatorNano,
		Currency:      "USD",
		CostPresent:   true,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:              lipapi.UsagePlaneProviderBillable,
			Source:             lipapi.UsageSourceProviderReported,
			Authority:          lipapi.UsageAuthorityAuthoritative,
			DedupeKey:          "refinement82-preterminal:provider-cost",
			ProviderAccountKey: "provider-account",
			ProviderRequestID:  "preterminal-request",
			ProviderChargeID:   "preterminal-charge",
		},
	}, "refinement82-preterminal.provider.v2")
	return stream
}

func (s *refinement82GatedUsageStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if i := int(s.pos.Add(1)) - 1; i < len(s.prefix) {
		return s.prefix[i], nil
	}
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-s.release:
	}
	if j := int(s.suffixPos.Add(1)) - 1; j < len(s.suffix) {
		return s.suffix[j], nil
	}
	return lipapi.Event{}, io.EOF
}

func (s *refinement82GatedUsageStream) Send(lipapi.Event) error { return nil }
func (s *refinement82GatedUsageStream) Close() error            { return nil }
func (s *refinement82GatedUsageStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

// DrainEconomicObservations adds the trusted customer account projection
// after ProviderEvidenceBuffer validation, mirroring the production adapter
// boundary without changing provider account/request/charge IDs.
func (s *refinement82GatedUsageStream) DrainEconomicObservations() []metering.Observation {
	if s == nil || s.ProviderEvidenceBuffer == nil {
		return nil
	}
	observations := s.ProviderEvidenceBuffer.DrainEconomicObservations()
	for i := range observations {
		observations[i].Subject.AccountID = s.accountID
		for j := range observations[i].Charges {
			observations[i].Charges[j].Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator}
		}
		if err := observations[i].Validate(); err != nil {
			return nil
		}
	}
	return observations
}

func injectRefinement82GatedBackend(t *testing.T, executor *coreruntime.Executor, accountID string, release chan struct{}, opens *atomic.Int32) {
	t.Helper()
	be := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return newRefinement82GatedUsageStream(accountID, release), nil
		},
	}
	if executor.Backends == nil {
		executor.Backends = map[string]execbackend.Backend{}
	}
	executor.Backends[billingHostLoopBackendID] = be
	capFn := func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
		return execbackend.EffectiveCaps(ctx, be, call, cand)
	}
	switch capMap := executor.CapsResolver.(type) {
	case capabilities.MapResolver:
		capMap[billingHostLoopBackendID] = capFn
	case nil:
		executor.CapsResolver = capabilities.MapResolver{billingHostLoopBackendID: capFn}
	default:
		t.Fatalf("CapsResolver type %T cannot accept refinement82 gated backend caps", executor.CapsResolver)
	}
}

// waitRefinement82LiveProviderCheckpoint polls the durable journal for the
// provider-authority B-leg observation while the backend stream is still
// gated (terminal not reached). It returns the live checkpoint observation
// whose subject carries the still-open B-leg identity.
//
//nolint:revive // test helper keeps t first per Go testing convention
func waitRefinement82LiveProviderCheckpoint(t *testing.T, parent context.Context, journal *journalstore.DurableStore, storeID string) metering.Observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{StoreID: storeID, ProviderAccountKey: "provider-account", Limit: 100})
		if err == nil {
			for _, observation := range page.Observations {
				if observation.Origin == metering.OriginProvider &&
					observation.Authority == metering.AuthorityObservedClaim &&
					observation.Subject.Kind == metering.SubjectBLeg &&
					strings.TrimSpace(observation.Subject.BLegID) != "" &&
					len(observation.Charges) != 0 {
					return observation
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for live preterminal provider checkpoint: err=%v", err)
		case <-ticker.C:
		}
	}
}

// TestRefinement82RuntimePreterminalCheckpointAdvancesProvider proves the
// real production preterminal path end to end: while one B-leg is still
// live (backend gated before terminal), its provider V2 usage travels
// ProviderEvidenceBuffer -> attempt drainSidebandEvidence ->
// queueEconomicCheckpoint -> first-flush-immediate journal+outbox append ->
// stock relay/workers. Observable production state must show, in order:
// rev1 (B-leg/call NOT closed, provider COGS posting + head exist, customer
// journal/balance unchanged), then rev2 terminal (sole terminal owner
// freezes execution with the SAME B-leg, head replay-stable, retail settles
// exactly once), then rev3 post-terminal correction (provider delta only,
// lifecycle stays closed). The test never calls AppendEconomicWork,
// AppendValuation, AppendProviderCost, call/leg append, or direct terminal
// store APIs; store readers only inspect results.
func TestRefinement82RuntimePreterminalCheckpointAdvancesProvider(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	storeID := "refinement82-preterminal"
	billingPath := filepath.Join(t.TempDir(), "billing.sqlite")
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	store := openRefinement52ConcurrentBillingStore(t, billingPath, storeID)
	catalog, _, _, operator := seedBillingHostLoopCatalog(t)
	if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding: %v", err)
	}
	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store: store, TerminalUsageSink: store, Catalog: catalog, Currency: "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 128000, true, nil },
		Strict:         true, ConservativeCeiling: &ceiling, PostTurnBatchSize: 1,
	})
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	configPath := writeRefinement52MeteredConfig(t, journalPath)
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath: configPath, Mandatory: lipsdk.StandardDistributionRequirements(), LogWriter: io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP, Production: prod,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)
	executor := hostActiveExecutor(t, host)
	journal, ok := executor.MeteringRecorder.(*journalstore.DurableStore)
	if !ok || journal == nil {
		t.Fatalf("stock metering recorder = %T, want durable journal", executor.MeteringRecorder)
	}
	if _, ok := executor.MeteringObservationSink.(metering.AtomicObservationSink); !ok {
		t.Fatalf("stock observation sink = %T, want atomic sink", executor.MeteringObservationSink)
	}
	accountID := "refinement82-preterminal-account"
	provisionBillingHostLoopAccount(t, store, accountID)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var opens atomic.Int32
	injectRefinement82GatedBackend(t, executor, accountID, release, &opens)
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})

	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: "refinement82-preterminal-session",
			ContinuityKey:          "refinement82-preterminal-session",
		},
		Route:    lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("preterminal economics")}}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Consume only the prefix: the backend stays gated, so no terminal event
	// can flow and the B-leg must remain open.
	var sawStarted bool
	for i := 0; i < 3; i++ {
		event, err := stream.Recv(ctx)
		if err != nil {
			t.Fatalf("prefix Recv %d: %v", i, err)
		}
		if event.Kind == lipapi.EventResponseStarted {
			sawStarted = true
		}
	}
	if !sawStarted {
		t.Fatal("prefix carried no response start")
	}

	// rev1 through the real preterminal seam: the provider checkpoint must be
	// durable while the B-leg/call are still open.
	checkpoint := waitRefinement82LiveProviderCheckpoint(t, ctx, journal, storeID)
	if checkpoint.Revision != 1 {
		t.Fatalf("preterminal checkpoint revision = %d, want 1", checkpoint.Revision)
	}
	liveBLegID := strings.TrimSpace(checkpoint.Subject.BLegID)
	if records, err := store.ListCallUsage(ctx, accountID); err != nil || len(records) != 0 {
		t.Fatalf("call closures while B-leg live = %+v, err = %v; want none", records, err)
	}
	initialWorks, err := prod.BillingObservationEconomicWorkBuilder.BuildEconomicRevisionWork(ctx, []metering.Observation{checkpoint})
	if err != nil {
		t.Fatalf("derive rev-1 work: %v", err)
	}
	var initialProvider billing.EconomicRevisionWork
	for _, work := range initialWorks {
		if work.Queue == billing.EconomicQueueProvider {
			initialProvider = work
			break
		}
	}
	if initialProvider.HeadKey == "" || initialProvider.EvidenceRevision != 1 {
		t.Fatalf("initial provider work = %+v, want revision 1", initialProvider)
	}
	preTerminalHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, initialProvider.HeadKey, 1, initialProvider.Input.InputSetHash)
	waitBillingHostLoopProviderCost(t, store, accountID)
	preterminalJournals := refinement4StockProviderJournals(t, store, accountID)
	if len(preterminalJournals) != 1 {
		t.Fatalf("preterminal provider journals = %d, want the rev-1 posting", len(preterminalJournals))
	}
	refinement82RequireProviderJournal(t, preterminalJournals[0], liveBLegID, refinement82OperatorNano)
	if got := refinement4StockCustomerTransactions(t, store, accountID); len(got) != 0 {
		t.Fatalf("customer posted while B-leg live: %+v", got)
	}
	preterminalAcct, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if preterminalAcct.BalanceNano != refinement82OpeningNano {
		t.Fatalf("balance while B-leg live = %d, want opening %d", preterminalAcct.BalanceNano, refinement82OpeningNano)
	}

	// rev2 terminal: release the gate, drain to DONE, and let the sole
	// terminal owner freeze the SAME B-leg. The head must stay replay-stable:
	// terminal is a completeness transition, not new provider economics.
	releaseOnce.Do(func() { close(release) })
	_ = drainBillingHostLoopStream(t, ctx, stream)
	if got := opens.Load(); got != 1 {
		t.Fatalf("backend opens = %d, want exactly one attempt", got)
	}
	closure, _, _ := waitBillingHostLoopCall(t, store, accountID)
	legs, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 1 || legs[0].BLegID != liveBLegID {
		t.Fatalf("terminal legs = %+v, want the same live B-leg %q", legs, liveBLegID)
	}
	if len(closure.ExpectedBLegIDs) != 1 || closure.ExpectedBLegIDs[0] != liveBLegID {
		t.Fatalf("closure B-leg set = %v, want [%s]", closure.ExpectedBLegIDs, liveBLegID)
	}
	headAfterTerminal, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, initialProvider.HeadKey)
	if err != nil {
		t.Fatalf("provider head after terminal: %v", err)
	}
	if headAfterTerminal.Fingerprint != preTerminalHead.Fingerprint || headAfterTerminal.HeadVersion != preTerminalHead.HeadVersion || headAfterTerminal.InputSetHash != preTerminalHead.InputSetHash {
		t.Fatalf("terminal handoff advanced the provider head: before=%+v after=%+v", preTerminalHead, headAfterTerminal)
	}
	exposure := waitRefinement4StockSettlement(t, ctx, store, accountID, closure.CallID)
	if exposure.IsOpen() {
		t.Fatalf("settlement left exposure open: %+v", exposure)
	}
	refinement82RequireCustomerSettlement(t, refinement4StockCustomerTransactions(t, store, accountID), refinement82CustomerNano)
	settledAcct, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if settledAcct.BalanceNano != refinement82OpeningNano-refinement82CustomerNano {
		t.Fatalf("balance after closure = %d, want %d", settledAcct.BalanceNano, refinement82OpeningNano-refinement82CustomerNano)
	}

	// rev3 post-terminal correction on the same closed B-leg through the
	// existing finalizer/correction seam: provider delta only, lifecycle
	// stays closed exactly once.
	lateAppender, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: store, Sink: executor.MeteringObservationSink, Resolver: journal,
	})
	if err != nil {
		t.Fatalf("NewLateEconomicAppender: %v", err)
	}
	legBefore := legs[0]
	authority := refinement52RuntimeProviderAuthority(t, legBefore)
	identity := coremetering.LateEconomicIdentity{
		StoreID: storeID, BillingCallID: closure.CallID, ALegID: closure.ALegID, BLegID: legBefore.BLegID,
		AttemptID: legBefore.BLegID, AttemptSeq: uint64(legBefore.AttemptSeq), ProviderID: legBefore.ProviderID,
		ProviderAccountKey: authority.Subject.ProviderAccountKey, ProviderRequestID: authority.Subject.ProviderRequestID, ProviderChargeID: authority.Subject.ProviderChargeID,
	}
	finalizer := refinement52RuntimeObservation(storeID, closure.CallID, closure.ALegID, legBefore, authority, "82-preterminal-finalizer", 2, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer}); err != nil {
		t.Fatalf("late provider finalizer: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	expectedFinalizer := refinement52ExpectedProviderWork(t, ctx, prod.BillingObservationEconomicWorkBuilder, journal, storeID, legBefore.BLegID, nil)
	finalizerHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedFinalizer.HeadKey, expectedFinalizer.EvidenceRevision, expectedFinalizer.Input.InputSetHash)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, expectedFinalizer.HeadKey, refinement82OperatorNano+refinement82FinalizerNano)
	legsAfter, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legsAfter) != 1 || legsAfter[0].Fingerprint != legBefore.Fingerprint {
		t.Fatalf("late evidence rewrote lifecycle leg: before=%+v after=%+v", legBefore, legsAfter)
	}
	if got := refinement4StockCustomerTransactions(t, store, accountID); len(got) != 1 {
		t.Fatalf("late evidence altered customer settlements: %+v", got)
	}
	finalJournals := refinement4StockProviderJournals(t, store, accountID)
	if len(finalJournals) != 2 || finalJournals[1].ReversalOf != finalJournals[0].ID {
		t.Fatalf("provider journals after finalizer = %+v, want chained initial + delta", finalJournals)
	}
	_ = finalizerHead
}
