package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 5.1 architecture ratchets for the corrected post-usage billing
// baseline. Each guard is written against the *semantics* of the corrected
// architecture (authoritative attempt sequence, customer/provider snapshot
// independence, request-scoped billing state, no monetary holds / no stream
// money), not formatting quirks, so a regression to positional/lexical
// ordering, operator-rate coupling in the customer path, or executor-global
// lifetime billing registries fails closed.

func TestPhase51AttemptSequenceAuthorityRatchets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := EvaluateBillingAttemptSequenceAuthority(root)
	if err != nil {
		t.Fatalf("evaluate sequence authority: %v", err)
	}
	if len(got) > 0 {
		t.Fatalf("attempt-sequence ratchets must pass against corrected code:\n%s", formatRatchetFindings(got))
	}
}

func TestPhase51CustomerOperatorIndependenceRatchets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := EvaluateBillingCustomerOperatorIndependence(root)
	if err != nil {
		t.Fatalf("evaluate customer/operator independence: %v", err)
	}
	if len(got) > 0 {
		t.Fatalf("customer rating must never resolve or carry operator rates:\n%s", formatRatchetFindings(got))
	}
}

func TestPhase51CallScopedStateOwnershipRatchets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := EvaluateBillingCallScopedStateOwnership(root)
	if err != nil {
		t.Fatalf("evaluate call-scoped state ownership: %v", err)
	}
	if len(got) > 0 {
		t.Fatalf("runtime billing bookkeeping must be call-scoped, not executor-global:\n%s", formatRatchetFindings(got))
	}
}

func TestPhase51HoldDeletionAndNoStreamMoneyRatchetsStayActive(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := EvaluateBillingHoldAndStreamMoneyLock(root)
	if err != nil {
		t.Fatalf("evaluate hold/stream-money lock: %v", err)
	}
	if len(got) > 0 {
		t.Fatalf("hold-deletion and no-stream-money ratchets must remain active and green:\n%s", formatRatchetFindings(got))
	}
}

func TestEvaluateBillingAttemptSequenceAuthorityDetectsPositionalReconstruction(t *testing.T) {
	t.Parallel()
	rel := "internal/core/billing/call_rating.go"
	root := t.TempDir()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	// Positional reconstruction: the rating adapter rebuilds sequence from the
	// slice index instead of the persisted B2BUA attempt sequence.
	body := `package billing

func RateCall(in CallRatingInput) (CallRatingResult, error) {
	legs := make([]LegUsageRecord, 0, len(in.Legs))
	for i, source := range in.Legs {
		legs = append(legs, LegUsageRecord{Seq: i + 1, BLegID: source.BLegID})
	}
	return CallRatingResult{}, nil
}
`
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EvaluateBillingAttemptSequenceAuthority(root)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("positional sequence reconstruction must be detected")
	}
	found := false
	for _, finding := range got {
		if finding.Rule == BillingCorrectnessRuleSequencePositional {
			found = true
		}
	}
	if !found {
		t.Fatalf("want positional-sequence finding, got:\n%s", formatRatchetFindings(got))
	}
}

func TestEvaluateBillingAttemptSequenceAuthorityAcceptsAuthoritativeAdapter(t *testing.T) {
	t.Parallel()
	rel := "internal/core/billing/call_rating.go"
	root := t.TempDir()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `package billing

func RateCall(in CallRatingInput) (CallRatingResult, error) {
	legs := make([]LegUsageRecord, 0, len(in.Legs))
	for _, source := range in.Legs {
		leg, err := source.Seal()
		if err != nil {
			return CallRatingResult{}, err
		}
		legs = append(legs, LegUsageRecord{Seq: leg.AttemptSeq, BLegID: leg.BLegID})
	}
	return CallRatingResult{}, nil
}
`
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ratingAbs := filepath.Join(root, filepath.FromSlash("internal/core/billing/rating.go"))
	if err := os.WriteFile(ratingAbs, []byte("package billing\n\nfunc latest(legs []LegUsageRecord) bool { return legs[0].Seq > legs[1].Seq }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EvaluateBillingAttemptSequenceAuthority(root)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("authoritative adapter must pass:\n%s", formatRatchetFindings(got))
	}

	badBody := strings.Replace(body, "Seq: leg.AttemptSeq", "Seq: 1", 1)
	if err := os.WriteFile(abs, []byte(badBody), 0o600); err != nil {
		t.Fatal(err)
	}
	bad, err := EvaluateBillingAttemptSequenceAuthority(root)
	if err != nil {
		t.Fatalf("evaluate violating adapter: %v", err)
	}
	if len(bad) == 0 {
		t.Fatal("violating sequence adapter must be detected")
	}
}

func TestEvaluateBillingCustomerOperatorIndependenceDetectsCoupling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rel       string
		body      string
		wantEmpty bool
	}{
		{
			name: "customer snapshots resolve operator rate",
			rel:  "internal/infra/billingcompose/catalog.go",
			body: `package billingcompose

func (c *SnapshotCatalog) CustomerRatingSnapshots(call billing.CallUsageRecord, legs []billing.CallLegUsageRecord) (CustomerRatingSnapshots, error) {
	rate, err := c.OperatorRate(legs[0].OperatorRateRef)
	if err != nil {
		return CustomerRatingSnapshots{}, err
	}
	_ = rate
	return CustomerRatingSnapshots{}, nil
}
`,
			wantEmpty: false,
		},
		{
			name: "customer resolver decoupled",
			rel:  "internal/infra/billingcompose/catalog.go",
			body: `package billingcompose

func (c *SnapshotCatalog) CustomerRatingSnapshots(call billing.CallUsageRecord, legs []billing.CallLegUsageRecord) (CustomerRatingSnapshots, error) {
	pricing, err := c.pricingSnapshot(call.CustomerPricingRef)
	if err != nil {
		return CustomerRatingSnapshots{}, err
	}
	cards, err := c.modelPricingForLegs(legs, pricing)
	if err != nil {
		return CustomerRatingSnapshots{}, err
	}
	return CustomerRatingSnapshots{DefaultPricing: pricing, ModelPricing: cards}, nil
}
`,
			wantEmpty: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			abs := filepath.Join(root, filepath.FromSlash(tt.rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			writeCustomerOperatorSupportFixtures(t, root)
			got, err := EvaluateBillingCustomerOperatorIndependence(root)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if tt.wantEmpty && len(got) != 0 {
				t.Fatalf("want no findings, got:\n%s", formatRatchetFindings(got))
			}
			if !tt.wantEmpty && len(got) == 0 {
				t.Fatal("want findings, got none")
			}
		})
	}
}

func writeCustomerOperatorSupportFixtures(t *testing.T, root string) {
	t.Helper()
	fixtures := map[string]string{
		"internal/infra/billingcompose/resolver.go": `package billingcompose

func (c *SnapshotCatalog) ResolveCallRating() {}
func (c *SnapshotCatalog) ResolveProviderCost() { var rate OperatorRate; _ = rate }
`,
		"internal/core/billing/call_post_usage_worker.go": `package billing
`,
		"internal/core/billing/call_rating.go": `package billing

type CallRatingInput struct{}
`,
		"internal/core/billing/rating.go": `package billing

type RatingInput struct{}
`,
	}
	for rel, body := range fixtures {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEvaluateBillingCallScopedStateOwnershipDetectsExecutorGlobalMap(t *testing.T) {
	t.Parallel()
	rel := "internal/core/runtime/executor.go"
	root := t.TempDir()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `package runtime

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"

type Executor struct {
	allocatedByCall map[string]int
	billingByCall   map[billing.BillingCallID]*billingCallState
	finalizeByKey   map[string]*finalizeCacheEntry
}

func (e *Executor) Execute() {}
`
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EvaluateBillingCallScopedStateOwnership(root)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("executor-global lifetime billing registries must be detected")
	}
}

func TestEvaluateBillingCallScopedStateOwnershipAcceptsCallScopedState(t *testing.T) {
	t.Parallel()
	rel := "internal/core/runtime/executor.go"
	root := t.TempDir()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `package runtime

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"

type Executor struct {
	Backends map[string]execbackend.Backend
}

func (e *Executor) Execute() {}
`
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EvaluateBillingCallScopedStateOwnership(root)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("legitimate Backends map must not be flagged:\n%s", formatRatchetFindings(got))
	}
}

func TestBillingCorrectnessRatchetRuleNamesReferenced(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{})
	for _, name := range []string{
		BillingCorrectnessRuleSequencePositional,
		BillingCorrectnessRuleSequenceTimestamp,
		BillingCorrectnessRuleSequenceLexical,
		BillingCorrectnessRuleSequenceAdapterAuthoritative,
		BillingCorrectnessRuleCustomerOperatorCoupling,
		BillingCorrectnessRuleCustomerInputCarriesOperatorRates,
		BillingCorrectnessRuleExecutorGlobalBillingRegistry,
		BillingCorrectnessRuleExecutorMapField,
		BillingCorrectnessRuleCallScopedStateOwnerMissing,
		BillingCorrectnessRuleHoldAndStreamMoneyLock,
	} {
		if strings.TrimSpace(name) == "" {
			t.Fatal("ratchet rule name must not be empty")
		}
		if _, exists := seen[name]; exists {
			t.Fatalf("duplicate ratchet rule name %q", name)
		}
		seen[name] = struct{}{}
	}
}
