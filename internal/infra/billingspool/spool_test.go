package billingspool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type recordingSink struct {
	mu       sync.Mutex
	calls    []billing.CallUsageRecord
	legs     []billing.CallLegUsageRecord
	err      error
	attempts int
}

type blockingSink struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSink) AppendCall(_ context.Context, _ billing.CallUsageRecord) error {
	close(s.entered)
	<-s.release
	return nil
}
func (s *blockingSink) AppendLeg(_ context.Context, _ billing.CallLegUsageRecord) error {
	close(s.entered)
	<-s.release
	return nil
}

func (s *recordingSink) AppendCall(_ context.Context, r billing.CallUsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, r)
	return nil
}
func (s *recordingSink) AppendLeg(_ context.Context, r billing.CallLegUsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if s.err != nil {
		return s.err
	}
	s.legs = append(s.legs, r)
	return nil
}

func spoolTestCall(t *testing.T, id string) billing.CallUsageRecord {
	t.Helper()
	callID, err := billing.ParseBillingCallID(id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	r, err := (billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: "account", ALegID: "a-leg", StartedAt: now, FinishedAt: now,
		Outcome:         billing.TurnOutcomeCompleted,
		ExpectedBLegIDs: []string{"b-leg"},
	}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSpoolAppendRequiresCommitAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "billing-spool.db")
	sink := &recordingSink{}
	spool, err := Open(context.Background(), Config{Path: path}, sink)
	if err != nil {
		t.Fatal(err)
	}
	call := spoolTestCall(t, "bc_00000000000000000000000000000001")
	if err := spool.AppendCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}

	spool, err = Open(context.Background(), Config{Path: path}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if got := spool.PendingCount(); got != 1 {
		t.Fatalf("pending after restart = %d, want 1", got)
	}
}

func TestSpoolReplayIsIdempotentAndConflictIsDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	sink := &recordingSink{}
	spool, err := Open(context.Background(), Config{Path: path}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	call := spoolTestCall(t, "bc_00000000000000000000000000000002")
	if err := spool.AppendCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if err := spool.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(sink.calls); got != 1 {
		t.Fatalf("central calls = %d, want 1", got)
	}
	conflict := call
	conflict.Outcome = billing.TurnOutcomeFailed
	if err := spool.AppendCall(context.Background(), conflict); !errors.Is(err, ErrFingerprintConflict) {
		t.Fatalf("conflict = %v, want fingerprint conflict", err)
	}
}

func TestSpoolCentralFailureBuffersAndHealthIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	sink := &recordingSink{err: errors.New("central unavailable")}
	spool, err := Open(context.Background(), Config{Path: path, MaxPendingRecords: 1}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if err := spool.AppendCall(context.Background(), spoolTestCall(t, "bc_00000000000000000000000000000003")); err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendCall(context.Background(), spoolTestCall(t, "bc_00000000000000000000000000000004")); !errors.Is(err, ErrPendingCapacity) {
		t.Fatalf("capacity = %v", err)
	}
	if err := spool.ProcessOnce(context.Background()); err == nil {
		t.Fatal("central outage must be reported")
	}
	health := spool.Health()
	if health.PendingRecords != 1 || health.State == HealthReady {
		t.Fatalf("health = %+v", health)
	}
}

func TestSpoolCommitAcknowledgementFailureDoesNotLoseCommittedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	sink := &recordingSink{}
	spool, err := Open(context.Background(), Config{Path: path, CommitAcknowledgedHook: func() error { return errors.New("crash before acknowledgement") }}, sink)
	if err != nil {
		t.Fatal(err)
	}
	call := spoolTestCall(t, "bc_00000000000000000000000000000006")
	if err := spool.AppendCall(context.Background(), call); err == nil {
		t.Fatal("acknowledgement fault must be returned")
	}
	if got := spool.PendingCount(); got != 1 {
		t.Fatalf("committed row count = %d, want 1", got)
	}
	_ = spool.Close()
}

func TestSpoolRetentionAndFreeDiskCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	sink := &recordingSink{}
	now := time.Unix(1000, 0).UTC()
	spool, err := Open(context.Background(), Config{Path: path, Now: func() time.Time { return now }, ProcessedRetention: time.Hour, FreeDiskBytes: func() int64 { return 0 }}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if err := spool.AppendCall(context.Background(), spoolTestCall(t, "bc_00000000000000000000000000000007")); !errors.Is(err, ErrFreeDiskCapacity) {
		t.Fatalf("free disk = %v", err)
	}
	spool.cfg.FreeDiskBytes = func() int64 { return 1 << 30 }
	if err := spool.AppendCall(context.Background(), spoolTestCall(t, "bc_00000000000000000000000000000008")); err != nil {
		t.Fatal(err)
	}
	if err := spool.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	spool.cfg.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if err := spool.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := spool.PendingCount(); got != 0 {
		t.Fatalf("retained pending rows = %d, want 0", got)
	}
}

func TestSpoolStaleDeliveryIsReclaimedAndExactlyOneWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	sink := &recordingSink{}
	spool, err := Open(context.Background(), Config{Path: path, ClaimTimeout: time.Nanosecond}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if err := spool.AppendCall(context.Background(), spoolTestCall(t, "bc_00000000000000000000000000000005")); err != nil {
		t.Fatal(err)
	}
	if err := spool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := spool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := spool.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("calls = %d, want one worker delivery", len(sink.calls))
	}
}

func TestSpoolProcessOnceSerializesClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
	spool, err := Open(context.Background(), Config{Path: path}, sink)
	if err != nil {
		t.Fatal(err)
	}
	call := spoolTestCall(t, "bc_00000000000000000000000000000011")
	if err := spool.AppendCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- spool.ProcessOnce(context.Background()) }()
	<-sink.entered

	closeDone := make(chan error, 1)
	go func() { closeDone <- spool.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while ProcessOnce was in delivery: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(sink.release)
	if err := <-processDone; err != nil {
		t.Fatalf("ProcessOnce = %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close = %v", err)
	}
}

func TestSpoolAppendCloseLifecycleDoesNotRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	spool, err := Open(context.Background(), Config{Path: path}, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	call := spoolTestCall(t, "bc_00000000000000000000000000000010")
	errs := make(chan error, 16)
	for range 16 {
		go func() { errs <- spool.AppendCall(context.Background(), call) }()
	}
	go func() { errs <- spool.Close() }()
	for range 17 {
		if err := <-errs; err != nil && !errors.Is(err, ErrSpoolClosed) {
			t.Errorf("append/close error = %v", err)
		}
	}
}

func TestSpoolHealthUsesInjectedClockAndReportsProbeErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	now := time.Unix(2000, 0).UTC()
	spool, err := Open(context.Background(), Config{Path: path, Now: func() time.Time { return now }}, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendCall(context.Background(), spoolTestCall(t, "bc_00000000000000000000000000000009")); err != nil {
		t.Fatal(err)
	}
	spool.cfg.Now = func() time.Time { return now.Add(90 * time.Minute) }
	health := spool.Health()
	if health.OldestPendingAge != 90*time.Minute {
		t.Fatalf("oldest pending age = %v, want 90m", health.OldestPendingAge)
	}
	if err := spool.db.Close(); err != nil {
		t.Fatal(err)
	}
	health = spool.Health()
	if health.State == HealthReady || health.ProbeError == "" {
		t.Fatalf("closed spool health = %+v, want probe error and degraded state", health)
	}
}

func TestSpoolDatabaseBytesIncludesWALSiblings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.db")
	sink := &recordingSink{}
	spool, err := Open(context.Background(), Config{Path: path}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	// With WAL mode active, the main database file plus -wal/-shm siblings must
	// all be counted; a WAL-only delta must therefore be reflected.
	base := spool.databaseBytes()
	if base <= 0 {
		t.Fatalf("databaseBytes = %d, want > 0", base)
	}
	walPath := path + "-wal"
	if st, err := os.Stat(walPath); err == nil && st.Size() > 0 {
		if got := spool.databaseBytes(); got <= st.Size() {
			t.Fatalf("databaseBytes = %d, want > wal size %d", got, st.Size())
		}
	}
}
