package authoritystore

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestMutationCoreClonesConfiguredTemplatesAndWindows(t *testing.T) {
	t.Parallel()
	window := domain.WindowSpec{Algorithm: domain.WindowAlgorithmFixed, Size: time.Hour, Anchor: time.Unix(0, 0)}
	store := &DurableStore{c: newStoreCore(Config{
		StoreID:     "template-isolation",
		RuleWindows: map[string]domain.WindowSpec{"active": window},
		LimitRows:   []controlplane.AccountingLimitStatusRow{{RuleID: "active", Limit: 10}},
	})}
	local := store.newMutationCore()
	local.limitTemplates["active"][0].Limit = 999
	local.limitTemplates["retired"] = []controlplane.AccountingLimitStatusRow{{RuleID: "retired", Limit: 1}}
	local.ruleWindows["active"] = domain.WindowSpec{Size: 2 * time.Hour}
	local.ruleWindows["retired"] = window

	if got := store.c.limitTemplates["active"][0].Limit; got != 10 {
		t.Fatalf("global active template limit = %d, want 10", got)
	}
	if _, ok := store.c.limitTemplates["retired"]; ok {
		t.Fatal("operation-local retired template leaked into global configuration")
	}
	if got := store.c.ruleWindows["active"]; got != window {
		t.Fatalf("global active window = %#v, want %#v", got, window)
	}
	if _, ok := store.c.ruleWindows["retired"]; ok {
		t.Fatal("operation-local retired window leaked into global configuration")
	}
}
