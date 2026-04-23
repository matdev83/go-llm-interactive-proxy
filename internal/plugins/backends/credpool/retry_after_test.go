package credpool_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
)

func TestCooldownFromRetryAfter_seconds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	got, ok := credpool.CooldownFromRetryAfter("120", now)
	if !ok {
		t.Fatal("expected ok")
	}
	want := now.Add(120 * time.Second)
	if !got.Equal(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	if !got.After(now) {
		t.Fatal("expected strictly after now")
	}
}

func TestCooldownFromRetryAfter_secondsTrimmed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	got, ok := credpool.CooldownFromRetryAfter("  60  ", now)
	if !ok {
		t.Fatal("expected ok")
	}
	if !got.Equal(now.Add(60*time.Second)) || !got.After(now) {
		t.Fatalf("unexpected %v ok=%v", got, ok)
	}
}

func TestCooldownFromRetryAfter_httpDate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	// RFC1123 example in the future relative to now
	header := "Fri, 24 Apr 2026 12:00:00 GMT"
	got, ok := credpool.CooldownFromRetryAfter(header, now)
	if !ok {
		t.Fatal("expected ok")
	}
	if !got.After(now) {
		t.Fatalf("want future instant, got %v", got)
	}
}

func TestCooldownFromRetryAfter_invalid(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	for _, v := range []string{"", " ", "-1", "not-a-number", "999999999999999999999"} {
		_, ok := credpool.CooldownFromRetryAfter(v, now)
		if ok {
			t.Fatalf("expected !ok for %q", v)
		}
	}
}

func TestCooldownFromRetryAfter_zeroSecondsRejected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	_, ok := credpool.CooldownFromRetryAfter("0", now)
	if ok {
		t.Fatal("zero delay must not produce ok (usable-at must be after now)")
	}
}

func TestCooldownFromRetryAfter_httpDateNotAfterNow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	past := "Wed, 22 Apr 2026 12:00:00 GMT"
	_, ok := credpool.CooldownFromRetryAfter(past, now)
	if ok {
		t.Fatal("past HTTP-date must be rejected")
	}
}

func TestCooldownFromRetryAfterOrFallback(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	fb := 90 * time.Second
	got := credpool.CooldownFromRetryAfterOrFallback("bogus", now, fb)
	if !got.Equal(now.Add(fb)) {
		t.Fatalf("want %v, got %v", now.Add(fb), got)
	}
	// valid header uses parser, not fallback
	got2 := credpool.CooldownFromRetryAfterOrFallback("30", now, fb)
	if !got2.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("want parsed 30s, got %v", got2)
	}
}

func TestCooldownFromRetryAfter_doesNotMutatePool(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{{Secret: "s"}}, fixedClock(base))
	if err != nil {
		t.Fatal(err)
	}
	c0, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.MarkRateLimited(c0.ID, base.Add(5*time.Minute))
	before := p.Snapshot(base)
	_, ok := credpool.CooldownFromRetryAfter("not-valid", base)
	if ok {
		t.Fatal("expected parse failure")
	}
	after := p.Snapshot(base)
	if len(before) != len(after) {
		t.Fatal("snapshot length changed")
	}
	if before[0].State != after[0].State || !before[0].CooldownUntil.Equal(after[0].CooldownUntil) {
		t.Fatalf("pool state mutated: before=%v after=%v", before[0], after[0])
	}
}
