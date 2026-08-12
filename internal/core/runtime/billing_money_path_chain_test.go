package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	_ "modernc.org/sqlite"
)

const (
	moneyPathHoldNano     int64 = 1000
	moneyPathOpeningNano  int64 = 10000
	moneyPathInputTokens        = 1_000_000
	moneyPathOutputTokens       = 1_000_000
	moneyPathCustomerNano int64 = 310 // 100 input + 200 output + 10 fixed
	moneyPathOperatorNano int64 = 125 // 50 input + 75 output
)

func TestBillingMoneyPathChainSuccessFinish(t *testing.T) {
	t.Parallel()
	harness := newMoneyPathHarness(t, "money-path-success")
	stream := harness.openUsageThenFinish(t)
	assertNoStreamPrices(t, drainStream(t, context.Background(), stream))
	harness.executor.WaitBillingHandoffRetries()
	harness.assertSealedPendingThenSettle(t, billing.TurnOutcomeCompleted)
}

func TestBillingMoneyPathChainCancelBillsObservedUsage(t *testing.T) {
	t.Parallel()
	harness := newMoneyPathHarness(t, "money-path-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream := harness.openUsageThenBlock(t, ctx)
	if err := drainUntilUsageThenCancel(ctx, cancel, stream); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel drain = %v, want context.Canceled", err)
	}
	_ = stream.Close()
	harness.executor.WaitBillingHandoffRetries()
	harness.assertSealedPendingThenSettle(t, billing.TurnOutcomeCanceled)
}

func TestBillingMoneyPathSealsHoldPricingRefsWhenIdentityDiverges(t *testing.T) {
	t.Parallel()
	harness := newMoneyPathHarness(t, "money-path-hold-refs")
	harness.executor.BillingIdentity.CustomerPricingRef = func(context.Context, lipapi.Call) billing.VersionRef {
		return billing.VersionRef{ID: "wrong-prices", Version: "v9"}
	}
	harness.executor.BillingIdentity.ChargePolicyRef = func(context.Context, lipapi.Call) billing.VersionRef {
		return billing.VersionRef{ID: "wrong-policy", Version: "v9"}
	}
	stream := harness.openUsageThenFinish(t)
	assertNoStreamPrices(t, drainStream(t, context.Background(), stream))
	harness.executor.WaitBillingHandoffRetries()
	pending := waitPendingProcessing(t, harness.store, harness.accountID)
	record, err := harness.store.GetUsageRecord(context.Background(), pending.TURKey)
	if err != nil {
		t.Fatal(err)
	}
	if record.CustomerPricingRef != harness.pricing.Ref {
		t.Fatalf("TUR CustomerPricingRef = %+v, want hold/pricing %+v", record.CustomerPricingRef, harness.pricing.Ref)
	}
	if record.ChargePolicyRef != harness.policy.Ref {
		t.Fatalf("TUR ChargePolicyRef = %+v, want hold/policy %+v", record.ChargePolicyRef, harness.policy.Ref)
	}
	if harness.authStore.auth.PricingRef != harness.pricing.Ref || harness.authStore.auth.ChargePolicyRef != harness.policy.Ref {
		t.Fatalf("hold refs = %+v / %+v", harness.authStore.auth.PricingRef, harness.authStore.auth.ChargePolicyRef)
	}
}

type moneyPathHarness struct {
	accountID string
	store     *billingstore.DurableStore
	authStore *capturingAuthorizationStore
	pricing   billing.PricingSnapshot
	policy    billing.ChargePolicy
	operator  billing.OperatorRateSnapshot
	executor  *runtime.Executor
}

func newMoneyPathHarness(t *testing.T, accountID string) *moneyPathHarness {
	t.Helper()
	dsn := fmt.Sprintf("file:runtime-billing-money-path-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", runtimeBillingTestSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "runtime-money-path"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAccount(context.Background(), billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: moneyPathOpeningNano, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}

	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 100, OutputPerMillionNano: 200,
		InputRatePresent: true, OutputRatePresent: true,
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	policy := billing.ChargePolicy{
		Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref,
		Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true, IncludeFixedCharges: true,
	}
	operator := billing.OperatorRateSnapshot{
		Ref: billing.VersionRef{ID: "operator-rates", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 50, OutputPerMillionNano: 75,
		InputRatePresent: true, OutputRatePresent: true,
	}
	identity := runtime.BillingIdentity{
		AccountID:          func(context.Context, lipapi.Call) string { return accountID },
		AuthorizationID:    func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth-" + aLeg },
		CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef { return pricing.Ref },
		ChargePolicyRef:    func(context.Context, lipapi.Call) billing.VersionRef { return policy.Ref },
		OperatorRateRef:    func(context.Context, string, string) billing.VersionRef { return operator.Ref },
	}
	ceiling := billing.Money{Nano: moneyPathHoldNano, Currency: "USD"}
	authStore := &capturingAuthorizationStore{inner: store}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: authStore, Releaser: store, Currency: "USD", Identity: identity,
		Policy:  func(context.Context, lipapi.Call) (billing.ChargePolicy, error) { return policy, nil },
		Pricing: func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		Strict:  true, ConservativeCeiling: &ceiling,
	})
	if err != nil {
		t.Fatal(err)
	}
	b2buaStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = b2buaStore
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAdmission = adapter
	ex.BillingTerminalHandoff = store
	ex.BillingHoldReleaser = store
	ex.BillingIdentity = identity
	t.Cleanup(ex.WaitBillingHandoffRetriesForClose)
	return &moneyPathHarness{
		accountID: accountID, store: store, authStore: authStore,
		pricing: pricing, policy: policy, operator: operator, executor: ex,
	}
}

func (h *moneyPathHarness) openUsageThenFinish(t *testing.T) lipapi.EventStream {
	t.Helper()
	return h.executeWithBackend(t, context.Background(), func() lipapi.ManagedEventStream {
		return lipapi.NewFixedEventStream(moneyPathUsageEvents(true))
	})
}

func (h *moneyPathHarness) openUsageThenBlock(t *testing.T, ctx context.Context) lipapi.EventStream {
	t.Helper()
	return h.executeWithBackend(t, ctx, func() lipapi.ManagedEventStream {
		return &usageThenBlockStream{events: moneyPathUsageEvents(false), ready: make(chan struct{})}
	})
}

func (h *moneyPathHarness) executeWithBackend(t *testing.T, ctx context.Context, openStream func() lipapi.ManagedEventStream) lipapi.EventStream {
	t.Helper()
	var opens atomic.Int32
	h.executor.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				h.assertHoldReservedNoRevenue(t)
				return openStream(), nil
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "backend:model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	stream, err := h.executor.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens = %d, want 1", opens.Load())
	}
	return stream
}

func (h *moneyPathHarness) assertHoldReservedNoRevenue(t *testing.T) {
	t.Helper()
	account, err := h.store.GetAccount(context.Background(), h.accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != moneyPathHoldNano || account.BalanceNano != moneyPathOpeningNano {
		t.Fatalf("pre-open account reserved=%d balance=%d, want reserved=%d balance=%d", account.ReservedNano, account.BalanceNano, moneyPathHoldNano, moneyPathOpeningNano)
	}
	journals, err := h.store.JournalTransactions(context.Background(), h.accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, journal := range journals {
		if journal.OperationKind == "customer_settlement" || journal.OperationKind == "provider_cogs" {
			t.Fatalf("money posted before execute: %+v", journal)
		}
	}
}

func (h *moneyPathHarness) assertSealedPendingThenSettle(t *testing.T, wantOutcome billing.TurnOutcome) {
	t.Helper()
	ctx := context.Background()
	pending := waitPendingProcessing(t, h.store, h.accountID)
	record, err := h.store.GetUsageRecord(ctx, pending.TURKey)
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != wantOutcome {
		t.Fatalf("TUR outcome = %q, want %q", record.Outcome, wantOutcome)
	}
	if len(record.Legs) != 1 || !record.Legs[0].Evidence.InputTokens.Present || !record.Legs[0].Evidence.OutputTokens.Present {
		t.Fatalf("sealed evidence missing input+output: %+v", record.Legs)
	}
	if record.Legs[0].Evidence.Cost.Present {
		t.Fatalf("stream price leaked onto LUR: %+v", record.Legs[0].Evidence.Cost)
	}
	if h.authStore.auth.ID == "" || h.authStore.auth.Amount.Nano != moneyPathHoldNano {
		t.Fatalf("captured authorization = %+v", h.authStore.auth)
	}

	worker, err := billing.NewPostTurnWorker(h.store, moneyPathRatingResolver{
		auth: h.authStore.auth, pricing: h.pricing, policy: h.policy, operator: h.operator,
	}, billing.PostTurnWorkerConfig{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}

	processing, err := h.store.GetProcessing(ctx, record.Key)
	if err != nil {
		t.Fatal(err)
	}
	if processing.Status != billing.ProcessingProcessed {
		t.Fatalf("processing = %+v, want processed", processing)
	}
	holds, err := h.store.QueryOpenHolds(ctx, h.accountID, billing.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(holds.Items) != 0 {
		t.Fatalf("open holds after settlement = %+v", holds.Items)
	}

	account, err := h.store.GetAccount(ctx, h.accountID)
	if err != nil {
		t.Fatal(err)
	}
	wantBalance := moneyPathOpeningNano - moneyPathCustomerNano
	spendable, err := account.SpendableNano()
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != wantBalance || account.ReservedNano != 0 || spendable != wantBalance {
		t.Fatalf("settled account balance=%d reserved=%d spendable=%d, want balance=spendable=%d reserved=0", account.BalanceNano, account.ReservedNano, spendable, wantBalance)
	}

	journals, err := h.store.JournalTransactions(ctx, h.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var customer, cogs, release bool
	var settleVersion uint64
	for _, journal := range journals {
		if err := journal.Validate(); err != nil {
			t.Fatalf("invalid journal: %v", err)
		}
		switch journal.OperationKind {
		case "customer_settlement":
			customer = true
			assertJournalSides(t, journal, "customer_financial_account", billing.JournalDebit, "usage_revenue", billing.JournalCredit, moneyPathCustomerNano)
			settleVersion = journal.SnapshotVersionAfter
		case "provider_cogs":
			cogs = true
			assertJournalSides(t, journal, "inference_provider_cogs", billing.JournalDebit, "provider_payable_clearing", billing.JournalCredit, moneyPathOperatorNano)
			if settleVersion != 0 && journal.SnapshotVersionAfter != settleVersion {
				t.Fatalf("COGS snapshot version %d, want same settlement version %d", journal.SnapshotVersionAfter, settleVersion)
			}
			settleVersion = journal.SnapshotVersionAfter
		case "authorization_release":
			release = true
			if settleVersion != 0 && journal.SnapshotVersionAfter != settleVersion {
				t.Fatalf("release snapshot version %d, want same settlement version %d", journal.SnapshotVersionAfter, settleVersion)
			}
		}
	}
	if !customer || !cogs || !release {
		t.Fatalf("missing settlement journals customer=%t cogs=%t release=%t count=%d", customer, cogs, release, len(journals))
	}

	report, err := h.store.AccountReport(ctx, h.accountID, billing.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if report.Account.BalanceNano != wantBalance || report.SpendableNano != wantBalance || report.Account.ReservedNano != 0 {
		t.Fatalf("account report = %+v", report)
	}
	explanation, err := h.store.TurnExplanation(ctx, record.Key)
	if err != nil {
		t.Fatal(err)
	}
	if explanation.Result.CustomerCharge.Nano != moneyPathCustomerNano || explanation.Result.ProviderCost.Nano != moneyPathOperatorNano {
		t.Fatalf("turn explanation result = %+v, want customer=%d operator=%d (journal, not stream events)", explanation.Result, moneyPathCustomerNano, moneyPathOperatorNano)
	}
	if explanation.Result.GrossMargin.Nano != moneyPathCustomerNano-moneyPathOperatorNano {
		t.Fatalf("gross margin = %+v", explanation.Result.GrossMargin)
	}
	if len(explanation.Snapshots) == 0 {
		t.Fatal("turn explanation missing operation snapshots")
	}
	for _, snapshot := range explanation.Snapshots {
		if snapshot.OperationKind == "customer_settlement" || snapshot.OperationKind == "provider_cogs" || snapshot.OperationKind == "authorization_release" {
			if snapshot.After.Version != account.Version {
				t.Fatalf("snapshot %s after version %d, want account version %d", snapshot.OperationKind, snapshot.After.Version, account.Version)
			}
		}
	}
}

type capturingAuthorizationStore struct {
	inner *billingstore.DurableStore
	auth  billing.Authorization
}

func (s *capturingAuthorizationStore) Authorize(ctx context.Context, in billing.AuthorizeInput) (billing.Authorization, error) {
	auth, err := s.inner.Authorize(ctx, in)
	if err == nil {
		s.auth = auth
	}
	return auth, err
}

type moneyPathRatingResolver struct {
	auth     billing.Authorization
	pricing  billing.PricingSnapshot
	policy   billing.ChargePolicy
	operator billing.OperatorRateSnapshot
}

func (r moneyPathRatingResolver) ResolveRating(_ context.Context, record billing.TurnUsageRecord) (billing.RatingInput, error) {
	return billing.RatingInput{
		Record: record, Authorization: r.auth, CustomerPricing: r.pricing,
		CustomerPolicy: r.policy, OperatorRates: billing.OperatorRateSet{r.operator},
	}, nil
}

func moneyPathUsageEvents(finish bool) []lipapi.Event {
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hello"},
		{
			Kind:          lipapi.EventUsageDelta,
			InputTokens:   moneyPathInputTokens,
			OutputTokens:  moneyPathOutputTokens,
			TotalTokens:   moneyPathInputTokens + moneyPathOutputTokens,
			UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
			Accounting: lipapi.UsageAccountingMetadata{
				Source:    lipapi.UsageSourceProviderReported,
				Authority: lipapi.UsageAuthorityAuthoritative,
			},
		},
	}
	if finish {
		events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
	}
	return events
}

func drainStream(t *testing.T, ctx context.Context, stream lipapi.EventStream) []lipapi.Event {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []lipapi.Event
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
}

func assertNoStreamPrices(t *testing.T, events []lipapi.Event) {
	t.Helper()
	var sawUsage bool
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		sawUsage = true
		if ev.CostPresent || ev.CostNanoUnits != 0 {
			t.Fatalf("stream price enrichment leaked: %+v", ev)
		}
	}
	if !sawUsage {
		t.Fatal("expected provider usage on the client stream")
	}
}

func assertJournalSides(t *testing.T, journal billing.JournalTransaction, debitAccount string, debitSide billing.JournalSide, creditAccount string, creditSide billing.JournalSide, nano int64) {
	t.Helper()
	if len(journal.Entries) != 2 {
		t.Fatalf("journal %s entries = %+v", journal.OperationKind, journal.Entries)
	}
	if journal.Entries[0].LedgerAccount != debitAccount || journal.Entries[0].Side != debitSide || journal.Entries[0].Amount.Nano != nano {
		t.Fatalf("journal %s debit = %+v, want %s %s %d", journal.OperationKind, journal.Entries[0], debitAccount, debitSide, nano)
	}
	if journal.Entries[1].LedgerAccount != creditAccount || journal.Entries[1].Side != creditSide || journal.Entries[1].Amount.Nano != nano {
		t.Fatalf("journal %s credit = %+v, want %s %s %d", journal.OperationKind, journal.Entries[1], creditAccount, creditSide, nano)
	}
}

func waitPendingProcessing(t *testing.T, store *billingstore.DurableStore, accountID string) billing.UsageRecordProcessing {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page, err := store.QueryProcessing(context.Background(), billing.ReportFilter{
			AccountID: accountID, Status: billing.ProcessingPending, Page: billing.PageRequest{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 {
			return page.Items[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for pending TUR processing row")
	return billing.UsageRecordProcessing{}
}

func drainUntilUsageThenCancel(ctx context.Context, cancel context.CancelFunc, stream lipapi.EventStream) error {
	sawUsage := false
	for {
		ev, err := stream.Recv(ctx)
		if err != nil {
			return err
		}
		if ev.Kind == lipapi.EventUsageDelta {
			if ev.CostPresent {
				return fmt.Errorf("stream price enrichment leaked on cancel path: %+v", ev)
			}
			sawUsage = true
			cancel()
			continue
		}
		if sawUsage {
			continue
		}
	}
}

type usageThenBlockStream struct {
	mu     sync.Mutex
	events []lipapi.Event
	ready  chan struct{}
	once   sync.Once
}

func (s *usageThenBlockStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	s.mu.Lock()
	if len(s.events) > 0 {
		ev := s.events[0]
		s.events = s.events[1:]
		s.mu.Unlock()
		return ev, nil
	}
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}

func (s *usageThenBlockStream) Close() error { return nil }

func (s *usageThenBlockStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

var _ lipapi.ManagedEventStream = (*usageThenBlockStream)(nil)
