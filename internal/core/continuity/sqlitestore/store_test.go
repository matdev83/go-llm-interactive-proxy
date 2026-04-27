package sqlitestore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

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
