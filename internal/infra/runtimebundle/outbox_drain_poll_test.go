package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	// outboxDrainStallWindow bounds consecutive polls with no relay progress,
	// not the total drain wall clock: a slow but steadily draining relay
	// passes, a frozen pending set fails fast. It reuses the previous fixed
	// 4s budget as the stall detector so stuck relays fail exactly as fast
	// as before.
	outboxDrainStallWindow = 4 * time.Second
	outboxDrainTick        = 20 * time.Millisecond
)

// listEconomicWorkIDs reads the immutable billing work identities already
// enqueued for one queue. A read error is returned as an error (never encoded
// as a signature) so a transient read failure cannot be mistaken for durable
// progress.
func listEconomicWorkIDs(ctx context.Context, store *billingstore.DurableStore, queue billing.EconomicQueue) ([]string, error) {
	var rows []struct {
		WorkID string `bun:"work_id"`
	}
	if err := store.DB().NewRaw(`SELECT work_id FROM billing_economic_work WHERE store_id = ? AND kind = ? ORDER BY work_id`,
		store.StoreID(), "economic_revision:"+string(queue)).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.WorkID)
	}
	return ids, nil
}

// observationRelayProgress fingerprints durable relay completion: the set of
// active observation-outbox item IDs plus the immutable work identities already
// enqueued. Mutable delivery/retry metadata (status, attempt count, lease
// owner, last error) is deliberately excluded, so a relay that only retries or
// churns cannot reset the caller's stall clock; only a durable completion event
// (a delivered outbox item or a new work identity) changes the fingerprint. A
// read failure returns an error, so read-error oscillation is likewise never
// mistaken for progress. "" means every appended observation has been relayed.
func observationRelayProgress(ctx context.Context, store *billingstore.DurableStore, journal *journalstore.DurableStore) (string, error) {
	pending, err := journal.ListPendingObservationOutbox(ctx, 64)
	if err != nil {
		return "", err
	}
	if len(pending) == 0 {
		return "", nil
	}
	ids := make([]string, 0, len(pending))
	for _, item := range pending {
		ids = append(ids, fmt.Sprintf("%d", item.ID))
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("outbox:")
	b.WriteString(strings.Join(ids, ","))
	for _, queue := range []billing.EconomicQueue{billing.EconomicQueueCustomer, billing.EconomicQueueProvider} {
		workIDs, workErr := listEconomicWorkIDs(ctx, store, queue)
		if workErr != nil {
			return "", workErr
		}
		fmt.Fprintf(&b, "|%s=%s", queue, strings.Join(workIDs, ","))
	}
	return b.String(), nil
}

// pollObservationOutboxDrained polls list until it reports drained (empty
// fingerprint, nil error). Each attempt runs under a child context bounded by
// the caller's parent and by a fresh stall window measured from the attempt
// start, so a retry after a read timeout always gets a usable context; the
// durable-progress clock is reset only by a successful read that observed a
// new fingerprint, never by a failed read. The stall detector fires when a
// successful read keeps observing the same non-empty fingerprint for a full
// stall window since the last durable progress, so a slow-but-progressing
// relay is tolerated while a frozen one fails fast. A read cancelled by its
// attempt deadline (or any read failure) is inconclusive: it is retried within
// the parent budget, and a permanently unreadable store surfaces as a
// parent-done failure rather than a false frozen outbox. This helper calls the
// callback synchronously, so it can bound only a callback that honours its
// context; it cannot bound a callback that ignores context cancellation.
func pollObservationOutboxDrained(parent context.Context, stallWindow, tick time.Duration, list func(context.Context) (string, error)) error {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	const unset = "\x00unset"
	lastSig, lastErr := unset, error(nil)
	lastProgress := time.Now()
	for {
		attemptDeadline := time.Now().Add(stallWindow)
		callCtx, cancel := context.WithDeadline(parent, attemptDeadline)
		sig, err := list(callCtx)
		cancel()
		now := time.Now()
		progressed := false
		if err == nil {
			if sig == "" {
				return nil
			}
			if sig != lastSig {
				// Durable progress is authoritative: a successful read that
				// observed a new fingerprint resets the stall clock even when
				// it completed after the stall deadline (scheduling or store
				// load must not erase observed progress).
				lastSig, lastErr = sig, nil
				lastProgress = now
				progressed = true
			}
		}
		if parent.Err() != nil {
			// Cancellation precedence: a done parent wins over stall
			// accounting, even amid endlessly changing fingerprints.
			if err != nil {
				return fmt.Errorf("outbox drain parent done: %w (last pending %q)", err, lastSig)
			}
			return fmt.Errorf("outbox drain parent done (last pending %q): %w", lastSig, parent.Err())
		}
		if err == nil && !progressed && now.Sub(lastProgress) >= stallWindow {
			return fmt.Errorf("outbox drain stalled with no progress for %s (last pending %q)", stallWindow, lastSig)
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-parent.Done():
			if lastErr != nil {
				return fmt.Errorf("outbox drain parent done: %w (last pending %q)", lastErr, lastSig)
			}
			return fmt.Errorf("outbox drain parent done (last pending %q): %w", lastSig, parent.Err())
		case <-ticker.C:
		}
	}
}

// Deterministic regression for the observation-outbox drain synchronization
// (Task20.1 Batch C follow-up): the relay drain wait must tolerate a slow but
// steadily progressing relay while still failing fast on a genuinely stalled
// one. A fixed total wall-clock deadline cannot distinguish the two — under
// full-suite load the relay legitimately needs longer than any fixed budget
// while holding valid leases, and the drain must not fail it.

// TestOutboxDrainPollCompletesThroughSlowProgress drives ~5s of steady
// progress with a 200ms stall window: a fixed-total deadline shorter than the
// work fails this pattern, progress-sensitive waiting passes it.
func TestOutboxDrainPollCompletesThroughSlowProgress(t *testing.T) {
	t.Parallel()
	const polls = 1000
	i := 0
	list := func(context.Context) (string, error) {
		s := fmt.Sprintf("pending-%d", i)
		if i < polls {
			i++
			return s, nil
		}
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pollObservationOutboxDrained(ctx, 200*time.Millisecond, 5*time.Millisecond, list); err != nil {
		t.Fatalf("steady progress must drain: %v", err)
	}
	if i < polls {
		t.Fatalf("drain returned after %d polls, want %d", i, polls)
	}
}

// TestOutboxDrainPollFailsFastOnStall pins the stuck-relay detector: frozen
// pending fails at the stall window instead of burning the parent budget.
func TestOutboxDrainPollFailsFastOnStall(t *testing.T) {
	t.Parallel()
	list := func(context.Context) (string, error) { return "stuck", nil }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	err := pollObservationOutboxDrained(ctx, 200*time.Millisecond, 5*time.Millisecond, list)
	if err == nil {
		t.Fatal("stalled pending must fail, got nil")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("stall failure must name the cause, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("stall failure took %v, want fast failure near the stall window", elapsed)
	}
}

// TestOutboxDrainPollBoundsBlockingCallback proves a callback that blocks on
// every read is bounded by the parent budget, not by an unbounded retry loop,
// and is reported as a parent-done failure rather than a frozen outbox.
func TestOutboxDrainPollBoundsBlockingCallback(t *testing.T) {
	t.Parallel()
	list := func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "blocked", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := pollObservationOutboxDrained(ctx, 50*time.Millisecond, 5*time.Millisecond, list)
	if err == nil {
		t.Fatal("blocking callback must fail, got nil")
	}
	if strings.Contains(err.Error(), "stalled") {
		t.Fatalf("a blocked read must not be reported as a frozen outbox: %v", err)
	}
	if !strings.Contains(err.Error(), "parent done") {
		t.Fatalf("blocking callback must be bounded by the parent, got: %v", err)
	}
}

// TestOutboxDrainPollAcceptsProgressAfterStallDeadline pins that a slow but
// successful read which observed a new durable fingerprint is progress, not a
// stall. Under load a store read can finish after the stall deadline while
// still reporting real relay progress, and that progress must reset the clock.
func TestOutboxDrainPollAcceptsProgressAfterStallDeadline(t *testing.T) {
	t.Parallel()
	const stallWindow = 50 * time.Millisecond
	calls := 0
	list := func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			time.Sleep(stallWindow + 25*time.Millisecond)
			return "pending-1", nil
		}
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pollObservationOutboxDrained(ctx, stallWindow, 5*time.Millisecond, list); err != nil {
		t.Fatalf("late durable progress must not be a stall: %v", err)
	}
}

// TestOutboxDrainPollBlockedReadIsInconclusiveNotStall pins that a store read
// cancelled by its attempt deadline is retried with a usable context instead
// of being reported as a frozen outbox: a contended single-connection read is
// no evidence of relay progress or of a freeze. The retry deliberately
// respects its context, so an already-expired retry context loops until the
// parent and fails this test.
func TestOutboxDrainPollBlockedReadIsInconclusiveNotStall(t *testing.T) {
	t.Parallel()
	const (
		stallWindow  = 50 * time.Millisecond
		recoveryWait = 10 * time.Millisecond
	)
	calls := 0
	list := func(ctx context.Context) (string, error) {
		calls++
		if calls == 1 {
			// First attempt blocks until its child deadline fires.
			<-ctx.Done()
			return "", ctx.Err()
		}
		// Every later attempt must receive a usable, not-already-expired
		// context so a read that respects ctx recovers once contention clears.
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(recoveryWait):
			return "", nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	if err := pollObservationOutboxDrained(ctx, stallWindow, 5*time.Millisecond, list); err != nil {
		t.Fatalf("a timed-out read must be retried with a usable context: %v", err)
	}
	if calls < 2 {
		t.Fatalf("blocked read was not retried: calls=%d", calls)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("blocked-read retry took %v, want fast recovery", elapsed)
	}
}

// TestOutboxDrainPollFrozenReadableStallsAtWindow pins frozen-outbox
// detection for a context-respecting callback: successful reads that keep
// observing the same non-empty fingerprint must trip the stall near the stall
// window, not run until the parent budget.
func TestOutboxDrainPollFrozenReadableStallsAtWindow(t *testing.T) {
	t.Parallel()
	const stallWindow = 100 * time.Millisecond
	list := func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "frozen", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	err := pollObservationOutboxDrained(ctx, stallWindow, 5*time.Millisecond, list)
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("a readable frozen outbox must trip the stall, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("frozen stall took %v, want near the %s stall window", elapsed, stallWindow)
	}
}

// TestOutboxDrainPollParentCancellationWins pins cancellation precedence:
// endlessly changing fingerprints must not outrun a done parent.
func TestOutboxDrainPollParentCancellationWins(t *testing.T) {
	t.Parallel()
	i := 0
	list := func(context.Context) (string, error) {
		i++
		return fmt.Sprintf("false-progress-%d", i), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := pollObservationOutboxDrained(ctx, 200*time.Millisecond, 5*time.Millisecond, list)
	if err == nil {
		t.Fatal("done parent must fail the wait, got nil")
	}
	if !strings.Contains(err.Error(), "parent done") {
		t.Fatalf("done parent must take precedence, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("cancelled wait took %v, want the parent budget", elapsed)
	}
}

// TestStockCompositionRelayWaitIgnoresNonProductiveChurn pins the boundedness
// guard for the stock composition relay wait: a frozen durable fingerprint and
// transient read-error oscillation must not reset the stall clock, so a
// permanently failing relay fails fast at the stall bound instead of running
// until the global go test timeout.
func TestStockCompositionRelayWaitIgnoresNonProductiveChurn(t *testing.T) {
	t.Parallel()
	const (
		stallWindow = 150 * time.Millisecond
		pollTick    = 5 * time.Millisecond
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	frozen := func(context.Context) (string, error) { return "outbox:id-1|provider=work-1", nil }
	start := time.Now()
	requireRelayStall(t, pollObservationOutboxDrained(ctx, stallWindow, pollTick, frozen), start)

	var calls atomic.Int32
	oscillating := func(context.Context) (string, error) {
		if calls.Add(1)%2 == 1 {
			return "outbox:id-1|provider=work-1", nil
		}
		return "", errors.New("transient relay read failure")
	}
	start = time.Now()
	requireRelayStall(t, pollObservationOutboxDrained(ctx, stallWindow, pollTick, oscillating), start)
}

func requireRelayStall(t *testing.T, err error, start time.Time) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("nonproductive relay must stall, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("stall took %v, want fast failure near the stall window", elapsed)
	}
}

// TestObservationRelayProgressIgnoresRetryChurn proves the production progress
// fingerprint only advances on durable completion. Mutable retry metadata
// (status, attempt count, lease owner, last error) must leave it unchanged, and
// a durable delivery must advance it to drained.
func TestObservationRelayProgressIgnoresRetryChurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	journal := newShadowJournal(t)
	store := newShadowBillingStore(t)

	observation := compositionObservation("progress-churn", 1)
	observation.Subject.StoreID = store.StoreID()
	observation.Correlation.StoreID = store.StoreID()
	if err := observation.Validate(); err != nil {
		t.Fatalf("churn observation invalid: %v", err)
	}
	sink := journalstore.NewObservationSinkWithOutbox(journal)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	if !ok {
		t.Fatalf("journal sink = %T, want atomic sink", sink)
	}
	if err := atomicSink.AppendObservations(ctx, []metering.Observation{observation}); err != nil {
		t.Fatalf("append observation: %v", err)
	}

	base, err := observationRelayProgress(ctx, store, journal)
	if err != nil {
		t.Fatalf("base progress: %v", err)
	}
	if !strings.HasPrefix(base, "outbox:") {
		t.Fatalf("base fingerprint = %q, want a pending outbox signature", base)
	}

	for i, status := range []string{"processing", "pending", "processing", "pending"} {
		if _, err := journal.DB().NewRaw(`UPDATE metering_observation_economic_outbox
SET status = ?, attempt_count = attempt_count + 1, lease_owner = ?, lease_until_unix = ?, last_error = ?
WHERE store_id = ?`, status, "churn-owner", time.Now().Add(time.Minute).UnixNano(), "churn-error-"+status, store.StoreID()).Exec(ctx); err != nil {
			t.Fatalf("churn %d: %v", i, err)
		}
		churned, err := observationRelayProgress(ctx, store, journal)
		if err != nil {
			t.Fatalf("churned progress %d: %v", i, err)
		}
		if churned != base {
			t.Fatalf("retry churn %d (%s) advanced the durable fingerprint: base=%q churned=%q", i, status, base, churned)
		}
	}

	if _, err := journal.DB().NewRaw(`UPDATE metering_observation_economic_outbox SET status = 'delivered' WHERE store_id = ?`, store.StoreID()).Exec(ctx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	drained, err := observationRelayProgress(ctx, store, journal)
	if err != nil {
		t.Fatalf("drained progress: %v", err)
	}
	if drained != "" {
		t.Fatalf("durable delivery fingerprint = %q, want drained empty", drained)
	}
}
