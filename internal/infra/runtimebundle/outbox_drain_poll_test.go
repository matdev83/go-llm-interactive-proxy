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
// fingerprint, nil error). The stall window bounds only consecutive polls
// with an unchanged pending set; steady progress extends the wait within the
// caller's parent budget. Every synchronous callback runs under a child
// context cut at the current stall deadline (an earlier parent deadline
// always wins), so a callback that blocks through the window cannot erase
// the stall: a late changed signature is still a stall, and only an actually
// drained result succeeds. A frozen set fails fast instead of burning the
// parent deadline, so a stuck relay is still detected promptly.
func pollObservationOutboxDrained(parent context.Context, stallWindow, tick time.Duration, list func(context.Context) (string, error)) error {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	const unset = "\x00unset"
	lastSig, lastErr := unset, error(nil)
	lastChange := time.Now()
	for {
		deadline := lastChange.Add(stallWindow)
		callCtx, cancel := context.WithDeadline(parent, deadline)
		sig, err := list(callCtx)
		cancel()
		now := time.Now()
		if err == nil && sig == "" {
			return nil
		}
		if parent.Err() != nil {
			// Cancellation precedence: a done parent wins over stall
			// accounting, even amid endlessly changing fingerprints.
			if err != nil {
				return fmt.Errorf("outbox drain parent done: %w (last pending %q)", err, lastSig)
			}
			return fmt.Errorf("outbox drain parent done (last pending %q): %w", lastSig, parent.Err())
		}
		if !now.Before(deadline) {
			// The callback blocked through the stall window: a changed
			// signature arriving late cannot reset the stall clock.
			if err != nil {
				return fmt.Errorf("outbox drain stalled: %w (last pending %q)", err, lastSig)
			}
			return fmt.Errorf("outbox drain stalled with no progress for %s (last pending %q)", stallWindow, lastSig)
		}
		if err != nil {
			lastErr = err
		} else if sig != lastSig {
			lastSig, lastErr = sig, nil
			lastChange = now
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

// TestOutboxDrainPollBoundsBlockingCallback proves a callback that blocks
// until its context fires is bounded by the stall window, not the long
// parent: with a 30s parent and a 200ms stall window the wait must return
// near 200ms with a stall error.
func TestOutboxDrainPollBoundsBlockingCallback(t *testing.T) {
	t.Parallel()
	list := func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "blocked", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	err := pollObservationOutboxDrained(ctx, 200*time.Millisecond, 5*time.Millisecond, list)
	if err == nil {
		t.Fatal("blocking callback must fail, got nil")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("blocked callback must report a stall, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("blocking callback took %v, want the stall window, not the parent budget", elapsed)
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
