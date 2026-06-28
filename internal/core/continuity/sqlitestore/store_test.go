package sqlitestore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/b2buatest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestOpenContext_rejectsNilContext(t *testing.T) {
	t.Parallel()
	_, err := OpenContext(nil, ":memory:") //nolint:staticcheck // contract: nil ctx must be rejected
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOpen_rejectsPathWithInvalidChars(t *testing.T) {
	t.Parallel()
	_, err := Open("./data/x?bad=1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNew_inMemory(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	leg, err := s.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if leg.ALegID == "" {
		t.Fatal("expected a-leg id")
	}
}

func TestStore_restartSurvival(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "continuity.db")

	ctx := context.Background()

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	leg, err := s1.CreateALeg(ctx, "session-xyz")
	if err != nil {
		t.Fatal(err)
	}
	bleg, err := s1.NextBLeg(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	rec := lipapi.AttemptRecord{
		BLegID:         bleg.BLegID,
		ALegID:         leg.ALegID,
		Seq:            bleg.Seq,
		BackendID:      "stub",
		EffectiveModel: "m",
		StartedAt:      time.Unix(1, 0),
		FinishedAt:     time.Unix(2, 0),
		Outcome:        lipapi.AttemptSuccess,
		Reason:         "ok",
	}
	if err := s1.RecordAttempt(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()

	got, err := s2.ResolveALeg(ctx, "session-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if got.ALegID != leg.ALegID {
		t.Fatalf("a-leg id %q want %q", got.ALegID, leg.ALegID)
	}
	rows, err := s2.LoadAttempts(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("attempts %+v", rows)
	}
	if rows[0].BackendID != "stub" || rows[0].Outcome != lipapi.AttemptSuccess {
		t.Fatalf("row %+v", rows[0])
	}
}

func TestFetchInterleavedState_RejectsCorruptStoredState(t *testing.T) {
	t.Parallel()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	leg, err := s.CreateALeg(ctx, "corrupt-interleaved")
	if err != nil {
		t.Fatal(err)
	}
	badJSON := `{"cycle":{"selector_key":"k","sequence":[{"key":"a","role":"executor"}],"next_index":5}}`
	if _, err := s.db.ExecContext(
		ctx,
		`UPDATE a_legs SET interleaved_state_json = ? WHERE a_leg_id = ?`,
		badJSON, leg.ALegID,
	); err != nil {
		t.Fatalf("inject corrupt state: %v", err)
	}
	_, err = s.FetchInterleavedState(ctx, leg.ALegID)
	if err == nil {
		t.Fatal("expected validation error for corrupt stored interleaved state")
	}
}

func TestStore_InterleavedState(t *testing.T) {
	t.Parallel()
	b2buatest.TestInterleavedStateStore(t, func(t *testing.T) b2buatest.Store {
		t.Helper()
		s, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestStore_InterleavedState_restartSurvival(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cont.db")
	ctx := context.Background()
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	leg, err := s1.CreateALeg(ctx, "durable-session")
	if err != nil {
		t.Fatal(err)
	}
	want := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sk",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "x", Role: interleavedstate.RoleExecutor},
				{Key: "t", Role: interleavedstate.RoleThinker},
			},
			NextIndex: 1,
		},
		MemoRef: &interleavedstate.MemoRef{Key: "m", Version: 2},
	}
	if err := s1.SetInterleavedState(ctx, leg.ALegID, want); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	got, err := s2.FetchInterleavedState(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("reopen fetch: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("durable round-trip mismatch: got %+v want %+v", got, want)
	}
}
