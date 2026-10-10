package runtimebundle_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	_ "modernc.org/sqlite"
)

func hostActiveExecutor(t *testing.T, host *runtimebundle.Host) *coreruntime.Executor {
	t.Helper()
	if host == nil {
		t.Fatal("nil host")
	}
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("nil active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("active generation missing ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatal("expected *runtime.Executor from active generation")
	}
	return ex
}

func writeBillingHostLoopConfig(t *testing.T) string {
	t.Helper()
	cfg := `server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "backend:model"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: backend
      kind: openai-responses
      enabled: false
      config: {}
    - id: bad
      kind: openai-responses
      enabled: false
      config: {}
    - id: good
      kind: openai-responses
      enabled: false
      config: {}
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`
	path := filepath.Join(t.TempDir(), "billing-host-loop.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var billingHostLoopSeq atomic.Uint64

func openBillingHostLoopStore(t *testing.T) *billingstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:billing-host-loop-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", billingHostLoopSeq.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "billing-host-loop"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func provisionBillingHostLoopAccount(t *testing.T, store billing.AccountProvisioner, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{
		ID:       accountID,
		Currency: "USD",
		Mode:     billing.AccountPrepaid,
		State:    billing.AccountReady,
		Version:  1,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := store.PostFunding(ctx, billing.FundingInput{
		AccountID: accountID,
		Amount:    billing.Money{Nano: billingHostLoopOpeningNano, Currency: "USD"},
		SourceKey: "opening-topup",
		Reason:    "host-loop prepaid funding",
	}); err != nil {
		t.Fatalf("PostFunding: %v", err)
	}
}
