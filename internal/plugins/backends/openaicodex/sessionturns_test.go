package openaicodex

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSessionTurnCounter_reserveIncrementsPerConversation(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 64)
	const n = 5
	for turn := 1; turn <= n; turn++ {
		if got := s.reserveTurn("conv-1"); got != turn {
			t.Fatalf("turn %d: reserveTurn=%d, want %d", turn, got, turn)
		}
	}
	if got := s.currentTurnNumber("conv-1"); got != n+1 {
		t.Fatalf("after %d reserves currentTurnNumber=%d, want %d", n, got, n+1)
	}
}

func TestSessionTurnCounter_perConversationIndependent(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 64)
	if got := s.reserveTurn("conv-a"); got != 1 {
		t.Fatalf("conv-a turn 1 reserveTurn=%d, want 1", got)
	}
	if got := s.reserveTurn("conv-b"); got != 1 {
		t.Fatalf("conv-b turn 1 reserveTurn=%d, want 1", got)
	}
	if got := s.reserveTurn("conv-a"); got != 2 {
		t.Fatalf("conv-a turn 2 reserveTurn=%d, want 2", got)
	}
	if got := s.currentTurnNumber("conv-b"); got != 2 {
		t.Fatalf("conv-b next turn=%d, want 2", got)
	}
}

func TestSessionTurnCounter_emptyConvIDNoOp(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 64)
	for range 10 {
		if got := s.reserveTurn(""); got != 0 {
			t.Fatalf("empty convID reserveTurn=%d, want 0", got)
		}
		s.releaseTurn("", 0)
		if got := s.currentTurnNumber(""); got != 0 {
			t.Fatalf("empty convID currentTurnNumber=%d, want 0", got)
		}
	}
}

func TestSessionTurnCounter_nilReceiverSafe(t *testing.T) {
	t.Parallel()
	var s *sessionTurnCounter
	if got := s.reserveTurn("conv-1"); got != 0 {
		t.Fatalf("nil receiver reserveTurn=%d, want 0", got)
	}
	s.releaseTurn("conv-1", 1)
	if got := s.currentTurnNumber("conv-1"); got != 0 {
		t.Fatalf("nil receiver currentTurnNumber=%d, want 0", got)
	}
}

func TestSessionTurnCounter_releaseRestoresSlot(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 64)
	if got := s.reserveTurn("conv-1"); got != 1 {
		t.Fatalf("reserveTurn=%d, want 1", got)
	}
	s.releaseTurn("conv-1", 1)
	if got := s.currentTurnNumber("conv-1"); got != 1 {
		t.Fatalf("after release currentTurnNumber=%d, want 1", got)
	}
	if got := s.reserveTurn("conv-1"); got != 1 {
		t.Fatalf("re-reserve after release=%d, want 1", got)
	}
}

func TestSessionTurnCounter_releasePartialKeepsPriorTurns(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 64)
	if got := s.reserveTurn("conv-1"); got != 1 {
		t.Fatalf("reserve 1=%d, want 1", got)
	}
	if got := s.reserveTurn("conv-1"); got != 2 {
		t.Fatalf("reserve 2=%d, want 2", got)
	}
	s.releaseTurn("conv-1", 2)
	if got := s.currentTurnNumber("conv-1"); got != 2 {
		t.Fatalf("after releasing turn 2, next=%d, want 2", got)
	}
	if got := s.reserveTurn("conv-1"); got != 2 {
		t.Fatalf("re-reserve after partial release=%d, want 2", got)
	}
}

func TestSessionTurnCounter_releaseOlderReservationDoesNotReuseLaterTurn(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 64)
	if got := s.reserveTurn("conv-1"); got != 1 {
		t.Fatalf("reserve 1=%d, want 1", got)
	}
	if got := s.reserveTurn("conv-1"); got != 2 {
		t.Fatalf("reserve 2=%d, want 2", got)
	}
	s.releaseTurn("conv-1", 1)
	if got := s.currentTurnNumber("conv-1"); got != 3 {
		t.Fatalf("after releasing the older reservation, next=%d, want 3", got)
	}
	if got := s.reserveTurn("conv-1"); got != 3 {
		t.Fatalf("re-reserve after older release=%d, want 3", got)
	}
	s.releaseTurn("conv-1", 2)
	s.releaseTurn("conv-1", 3)
	if got := s.currentTurnNumber("conv-1"); got != 1 {
		t.Fatalf("after releasing both reservations, next=%d, want 1", got)
	}
}

func TestSessionTurnCounter_ttlExpiryResetsWindow(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := newSessionTurnCounter(time.Hour, 64)
	s.now = func() time.Time { return now }
	if got := s.reserveTurn("conv-1"); got != 1 {
		t.Fatalf("turn 1 reserveTurn=%d, want 1", got)
	}
	if got := s.reserveTurn("conv-1"); got != 2 {
		t.Fatalf("turn 2 reserveTurn=%d, want 2", got)
	}
	now = now.Add(2 * time.Hour)
	if got := s.currentTurnNumber("conv-1"); got != 1 {
		t.Fatalf("after TTL expiry, currentTurnNumber=%d, want 1", got)
	}
	if got := s.reserveTurn("conv-1"); got != 1 {
		t.Fatalf("after TTL expiry reserveTurn=%d, want 1", got)
	}
}

func TestSessionTurnCounter_maxEntriesEviction(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 3)
	for _, conv := range []string{"conv-a", "conv-b", "conv-c"} {
		if got := s.reserveTurn(conv); got != 1 {
			t.Fatalf("conv %s reserveTurn=%d, want 1", conv, got)
		}
	}
	if got := s.reserveTurn("conv-d"); got != 1 {
		t.Fatalf("conv-d reserveTurn=%d, want 1", got)
	}
	if got := s.currentTurnNumber("conv-a"); got != 1 {
		t.Fatalf("evicted conv-a should re-enter at turn 1, got %d", got)
	}
}

func TestSessionTurnCounter_concurrentReserveUniqueTurns(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 2048)
	const n = 200
	results := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			results[i] = s.reserveTurn("conv-race")
		}(i)
	}
	wg.Wait()
	seen := make(map[int]struct{}, n)
	for _, turn := range results {
		if turn < 1 || turn > n {
			t.Fatalf("reserveTurn out of range: %d", turn)
		}
		if _, ok := seen[turn]; ok {
			t.Fatalf("duplicate reserved turn %d", turn)
		}
		seen[turn] = struct{}{}
	}
	if len(seen) != n {
		t.Fatalf("unique turns=%d, want %d", len(seen), n)
	}
	if got := s.currentTurnNumber("conv-race"); got != n+1 {
		t.Fatalf("after concurrent reserves currentTurnNumber=%d, want %d", got, n+1)
	}
}

func TestSessionTurnCounter_concurrentReserveReleaseStable(t *testing.T) {
	t.Parallel()
	s := newSessionTurnCounter(time.Hour, 2048)
	const n = 100
	var wg sync.WaitGroup
	var zeroReserves atomic.Int64
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			turn := s.reserveTurn("conv-release")
			if turn == 0 {
				zeroReserves.Add(1)
				return
			}
			s.releaseTurn("conv-release", turn)
		}()
	}
	wg.Wait()
	if got := zeroReserves.Load(); got != 0 {
		t.Fatalf("reserveTurn returned 0 %d times", got)
	}
	if got := s.currentTurnNumber("conv-release"); got != 1 {
		t.Fatalf("after matched reserve/release pairs currentTurnNumber=%d, want 1", got)
	}
}

func TestVerbosityTurnsEnabled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "both enabled", want: true},
		{name: "early disabled", cfg: Config{EarlySessionVerbosityBumpDisabled: true}, want: true},
		{name: "mid disabled", cfg: Config{MidSessionVerbosityBumpDisabled: true}, want: true},
		{
			name: "both disabled",
			cfg: Config{
				EarlySessionVerbosityBumpDisabled: true,
				MidSessionVerbosityBumpDisabled:   true,
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := verbosityTurnsEnabled(tc.cfg); got != tc.want {
				t.Fatalf("verbosityTurnsEnabled=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionTurnKey_staysOnContinuityKeyWhenAuthoritativeSessionIDChanges(t *testing.T) {
	t.Parallel()
	gotA := sessionTurnKey(lipapi.Call{
		ID: "call-a",
		Session: lipapi.SessionRef{
			ContinuityKey:          "continuity-a",
			AuthoritativeSessionID: "auth-1",
		},
	}, "fallback")
	gotB := sessionTurnKey(lipapi.Call{
		ID: "call-b",
		Session: lipapi.SessionRef{
			ContinuityKey:          "continuity-a",
			AuthoritativeSessionID: "auth-2",
		},
	}, "fallback")
	if gotA != "continuity:continuity-a" || gotB != "continuity:continuity-a" {
		t.Fatalf("sessionTurnKey continuity precedence = %q / %q, want continuity:continuity-a", gotA, gotB)
	}
}

func TestSessionTurnKey_fallsBackToCorrelationIDWhenContinuityMissing(t *testing.T) {
	t.Parallel()
	got := sessionTurnKey(lipapi.Call{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: "auth-1",
		},
	}, "fallback")
	if got != "session:auth-1" {
		t.Fatalf("sessionTurnKey correlation fallback = %q, want session:auth-1", got)
	}
}
