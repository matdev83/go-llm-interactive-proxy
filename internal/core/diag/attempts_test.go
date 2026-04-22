package diag_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAttemptsHandler_missingParam(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ah, err := diag.AttemptsHandler(st)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ah)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestAttemptsHandler_notFound(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ah, err := diag.AttemptsHandler(st)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ah)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "?a_leg_id=does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestAttemptsHandler_ok(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.NextBLeg(ctx, a.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	rec := lipapi.AttemptRecord{
		BLegID:         b.BLegID,
		ALegID:         a.ALegID,
		Seq:            b.Seq,
		BackendID:      "openai",
		EffectiveModel: "gpt-4",
		StartedAt:      now,
		FinishedAt:     now,
		Outcome:        lipapi.AttemptSuccess,
		Reason:         "",
	}
	if err := st.RecordAttempt(ctx, rec); err != nil {
		t.Fatal(err)
	}
	ah, err := diag.AttemptsHandler(st)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ah)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "?a_leg_id=" + a.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type: %q", ct)
	}
	var rows []lipapi.AttemptRecord
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows)=%d", len(rows))
	}
	if rows[0].BLegID != b.BLegID || rows[0].Seq != b.Seq {
		t.Fatalf("row: %+v", rows[0])
	}
}
