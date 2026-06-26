package openaicodex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
)

func testStore(t *testing.T, strategy string, ttl time.Duration, max int, now time.Time) *accountStore {
	t.Helper()
	creds := []credpool.Credential{
		{ID: "a.json", Secret: "tok-a", RemoteAccountID: "a"},
		{ID: "b.json", Secret: "tok-b", RemoteAccountID: "b"},
	}
	pool, err := credpool.New(creds)
	if err != nil {
		t.Fatal(err)
	}
	return &accountStore{
		pool: pool,
		meta: []accountMeta{
			{poolID: "a.json", ID: "a", FilePath: "a.json"},
			{poolID: "b.json", ID: "b", FilePath: "b.json"},
		},
		strategy:      strategy,
		affinityTTL:   ttl,
		affinityMax:   max,
		fallback:      time.Minute,
		affinity:      make(map[string]affinityEntry),
		affinityOrder: []string{},
		now:           func() time.Time { return now },
	}
}

func TestNewAccountStore_duplicateAccountIDsDifferentFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccount := func(name, token string) {
		path := filepath.Join(dir, name)
		body := `{"account_id":"shared-acct","access_token":"` + token + `"}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeAccount("first.json", "tok-first")
	writeAccount("second.json", "tok-second")

	store, err := newAccountStore(Config{ManagedOAuthStoragePath: dir})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	store.now = func() time.Time { return now }

	first, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "shared-acct" {
		t.Fatalf("ID = %q want shared-acct", first.ID)
	}

	store.markAuthInvalid(first)

	second, err := store.selectAccountForSession("sess-2")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != "shared-acct" {
		t.Fatalf("ID = %q want shared-acct", second.ID)
	}
	if second.AccessToken == first.AccessToken {
		t.Fatalf("expected different token after marking first invalid: first=%q second=%q", first.AccessToken, second.AccessToken)
	}
}

func TestSelectAccountForSession_sessionAffinityReusesAccount(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	store := testStore(t, selectionSessionAffinity, time.Hour, 100, now)

	first, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("same session: first=%q second=%q", first.ID, second.ID)
	}
}

func TestSelectAccountForSession_differentSessionsCanDiffer(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	store := testStore(t, selectionSessionAffinity, time.Hour, 100, now)

	a, err := store.selectAccountForSession("sess-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.selectAccountForSession("sess-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("expected different accounts for different sessions, both %q", a.ID)
	}
}

func TestSelectAccountForSession_rotatesWhenAccountUnusable(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	store := testStore(t, selectionSessionAffinity, time.Hour, 100, now)

	first, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	store.markAuthInvalid(first)

	second, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected rotation after auth invalid, still %q", second.ID)
	}

	third, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if third.ID != second.ID {
		t.Fatalf("expected sticky after rotation: got %q want %q", third.ID, second.ID)
	}
}

func TestSelectAccountForSession_rotatesOnCooldown(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	store := testStore(t, selectionSessionAffinity, time.Hour, 100, now)

	first, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	store.markRateLimited(first, now.Add(time.Hour))

	second, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected rotation after cooldown, still %q", first.ID)
	}
}

func TestSelectAccountForSession_affinityExpiresAfterTTL(t *testing.T) {
	t.Parallel()
	start := time.Unix(1000, 0)
	store := testStore(t, selectionSessionAffinity, 30*time.Minute, 100, start)

	first, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}

	store.now = func() time.Time { return start.Add(31 * time.Minute) }
	second, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.affinity["sess-1"]; !ok {
		t.Fatal("expected new affinity entry after TTL expiry")
	}
	entry := store.affinity["sess-1"]
	if entry.boundAt != store.now() {
		t.Fatalf("expected rebound at now, boundAt=%v now=%v", entry.boundAt, store.now())
	}
	_ = first
	_ = second
}

func TestSelectAccountForSession_emptySessionFallsBackWithoutAffinity(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	store := testStore(t, selectionSessionAffinity, time.Hour, 100, now)

	first, err := store.selectAccountForSession("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.selectAccountForSession("")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("empty session should use first-available: first=%q second=%q", first.ID, second.ID)
	}
	if len(store.affinity) != 0 {
		t.Fatalf("empty session should not store affinity: %#v", store.affinity)
	}
}

func TestSelectAccountForSession_maxEntriesEvictsOldest(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	store := testStore(t, selectionSessionAffinity, time.Hour, 2, now)

	_, err := store.selectAccountForSession("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(time.Second) }
	_, err = store.selectAccountForSession("sess-2")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(2 * time.Second) }
	_, err = store.selectAccountForSession("sess-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.affinity) != 2 {
		t.Fatalf("affinity len = %d want 2", len(store.affinity))
	}
	if _, ok := store.affinity["sess-1"]; ok {
		t.Fatal("sess-1 should have been evicted")
	}
	if _, ok := store.affinity["sess-2"]; !ok {
		t.Fatal("sess-2 should remain")
	}
	if _, ok := store.affinity["sess-3"]; !ok {
		t.Fatal("sess-3 should remain")
	}
}

func TestPersistQuotaHeaders_doesNotBlockOnMutexForFileIO(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")
	if err := os.WriteFile(path, []byte(`{"account_id":"a","access_token":"tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := credpool.New([]credpool.Credential{{ID: path, Secret: "tok", RemoteAccountID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	store := &accountStore{
		pool: pool,
		meta: []accountMeta{{poolID: path, ID: "a", FilePath: path}},
	}
	acct := managedAccount{poolID: path, ID: "a", AccessToken: "tok", FilePath: path}
	headers := map[string]string{"x-codex-plan-type": "pro"}

	store.mu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- store.persistQuotaHeaders(acct, headers)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), "quota_headers") {
			store.mu.Unlock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		runtime.Gosched()
	}
	store.mu.Unlock()
	t.Fatal("quota headers not written while store mutex held")
}

func writeManagedAccountFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadManagedAccounts_sortedByFilePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManagedAccountFile(t, dir, "z.json", `{"account_id":"z","access_token":"tok-z"}`)
	writeManagedAccountFile(t, dir, "a.json", `{"account_id":"a","access_token":"tok-a"}`)
	writeManagedAccountFile(t, dir, "m.json", `{"account_id":"m","access_token":"tok-m"}`)

	got, err := loadManagedAccounts(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d want 3", len(got))
	}
	want := []string{"a", "m", "z"}
	for i, acct := range got {
		if !strings.HasSuffix(acct.FilePath, want[i]+".json") {
			t.Fatalf("index %d path = %q want suffix %q.json", i, acct.FilePath, want[i])
		}
	}
}

func TestLoadManagedAccounts_filterByAccountID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManagedAccountFile(t, dir, "a.json", `{"account_id":"acct-a","access_token":"tok-a"}`)
	writeManagedAccountFile(t, dir, "b.json", `{"account_id":"acct-b","access_token":"tok-b"}`)

	got, err := loadManagedAccounts(dir, []string{"acct-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d want 1", len(got))
	}
	if got[0].ID != "acct-b" {
		t.Fatalf("ID = %q want acct-b", got[0].ID)
	}
}

func TestLoadManagedAccounts_filterByFilename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManagedAccountFile(t, dir, "pick-me.json", `{"account_id":"acct-a","access_token":"tok-a"}`)
	writeManagedAccountFile(t, dir, "skip-me.json", `{"account_id":"acct-b","access_token":"tok-b"}`)

	got, err := loadManagedAccounts(dir, []string{"pick-me"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d want 1", len(got))
	}
	if !strings.HasSuffix(got[0].FilePath, "pick-me.json") {
		t.Fatalf("path = %q want pick-me.json", got[0].FilePath)
	}
}

func TestSelectAccountForSession_roundRobinSkipsCooldown(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	store := testStore(t, selectionRoundRobin, 0, 0, now)

	first, err := store.selectAccountForSession("")
	if err != nil {
		t.Fatal(err)
	}
	store.markRateLimited(first, now.Add(time.Hour))

	for i := 0; i < 3; i++ {
		acct, err := store.selectAccountForSession("")
		if err != nil {
			t.Fatal(err)
		}
		if acct.ID == first.ID {
			t.Fatalf("round %d: cooldown account %q should be skipped", i, acct.ID)
		}
	}
}

func TestPersistQuotaHeaders_updatesCachedPlanType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")
	if err := os.WriteFile(path, []byte(`{"account_id":"a","access_token":"tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := credpool.New([]credpool.Credential{{ID: path, Secret: "tok", RemoteAccountID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	store := &accountStore{
		pool: pool,
		meta: []accountMeta{{poolID: path, ID: "a", FilePath: path}},
	}
	acct := managedAccount{poolID: path, ID: "a", AccessToken: "tok", FilePath: path}
	headers := map[string]string{
		"x-codex-plan-type":            "pro",
		"x-codex-primary-used-percent": "42",
	}

	if err := store.persistQuotaHeaders(acct, headers); err != nil {
		t.Fatal(err)
	}
	if store.meta[0].PlanType != "pro" {
		t.Fatalf("PlanType = %q want pro", store.meta[0].PlanType)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	qh, ok := saved["quota_headers"]
	if !ok {
		t.Fatalf("missing quota_headers: %s", b)
	}
	var got map[string]string
	if err := json.Unmarshal(qh, &got); err != nil {
		t.Fatal(err)
	}
	if got["x-codex-plan-type"] != "pro" {
		t.Fatalf("plan type: %q", got["x-codex-plan-type"])
	}
	if got["x-codex-primary-used-percent"] != "42" {
		t.Fatalf("usage percent: %q", got["x-codex-primary-used-percent"])
	}
}
