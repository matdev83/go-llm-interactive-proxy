package billing_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	billingadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/billing"

	_ "modernc.org/sqlite"
)

var repairE2ESeq atomic.Int64

func TestExposureRepairHTTPIncompleteAgainstDurableStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dsn := fmt.Sprintf("file:billing-repair-http-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", repairE2ESeq.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(16)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(ctx, bunDB, billingstore.Config{StoreID: "repair-http"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	account := corebilling.Account{ID: "http-repair", Currency: "USD", Mode: corebilling.AccountPrepaid, BalanceNano: 100, State: corebilling.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	pricing := corebilling.VersionRef{ID: "p", Version: "1"}
	policy := corebilling.VersionRef{ID: "c", Version: "1"}
	if _, err := store.AdmitExposure(ctx, corebilling.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: corebilling.Money{Nano: 25, Currency: "USD"},
		PricingRef: pricing, ChargePolicyRef: policy,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, corebilling.CallUsageRecord{
		SchemaVersion: corebilling.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-http",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), Outcome: corebilling.TurnOutcomeFailed,
		CustomerPricingRef: pricing, ChargePolicyRef: policy, ExpectedBLegIDs: []string{"b-missing"},
	}); err != nil {
		t.Fatal(err)
	}

	h := billingadmin.NewHandler(billingadmin.Options{Recovery: store})
	body := `{"call_id":"` + callID.String() + `","source_key":"http-op-1","mode":"incomplete"}`
	req := httptest.NewRequest(http.MethodPost, "/exposure-repair", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var settled corebilling.CallSettlement
	if err := json.Unmarshal(rec.Body.Bytes(), &settled); err != nil {
		t.Fatal(err)
	}
	if settled.CallID != callID {
		t.Fatalf("settled call=%s want %s", settled.CallID, callID)
	}
	closed, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.IsOpen() {
		t.Fatal("exposure must be closed after HTTP incomplete repair")
	}
}
