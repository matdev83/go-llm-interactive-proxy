package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBillingCoreStaysProviderAndPersistenceFree locks Req 17.7/17.8 and design
// Fail-if "billing imports provider SDKs": internal/core/billing owns TUR/LUR
// policy only and must not pull provider SDKs, lipapi wire types, SQL/Bun, or
// composition roots.
func TestBillingCoreStaysProviderAndPersistenceFree(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/billing"}, []forbiddenDep{
		{Substr: "github.com/openai/openai-go", ErrMsg: "billing must not import OpenAI provider SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "billing must not import Anthropic provider SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "billing must not import Gemini provider SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "billing must not import AWS provider SDK"},
		{Substr: "/pkg/lipapi", ErrMsg: "billing must not import lipapi (provider-neutral evidence only)"},
		{Substr: "database/sql", ErrMsg: "billing must not import database/sql"},
		{Substr: "uptrace/bun", ErrMsg: "billing must not import Bun persistence"},
		{Substr: "/internal/infra/", ErrMsg: "billing must not import infra adapters"},
		{Substr: "/internal/infra/runtimebundle", ErrMsg: "billing must not import composition runtimebundle"},
		{Substr: "/internal/plugins/", ErrMsg: "billing must not import concrete plugins"},
	})
}

// TestRuntimeStreamHandlersStayOffJournalRatingSettlement locks 7.1 / 17.8
// (already active) and design Fail-if "runtime stream handlers call rating/journal/settlement".
func TestRuntimeStreamHandlersStayOffJournalRatingSettlement(t *testing.T) {
	t.Parallel()

	assertDirectImportsExclude(t, "./internal/core/runtime", "/internal/infra/billingstore",
		"runtime must not import billingstore journal settlement")
	assertDirectImportsExclude(t, "./internal/core/runtime", "/internal/infra/billingadmission",
		"runtime must not import billing admission store adapters")
	assertDirectImportsExclude(t, "./internal/core/runtime", "/internal/core/tokenaccounting/ledger",
		"runtime must not import legacy token ledger")

	root := repoRoot(t)
	streamFiles := []string{
		"executor_recv_loop.go",
		"executor_settlement.go",
		"executor_retry_stream.go",
		"response_pipeline_observations.go",
		"stream_terminal.go",
		"turn_terminal.go",
		"parallel_race.go",
	}
	forbiddenIdents := []string{
		"rateMonetaryExposure",
		"rateMonetaryExposureWith",
		"EconomicsRater",
		"ApplyBillingResult",
		"PostJournalTransaction",
		"CustomerSettlementSourceKey",
		"ProviderCostSourceKey",
		"enrichUsageCost",
		"recordTokenAccountingLedger",
		"recordPartialTokenAccountingLedger",
		"recordCancellationBillingMarker",
		"openDurableAccountingLedger",
	}
	dir := filepath.Join(root, "internal", "core", "runtime")
	fset := token.NewFileSet()
	for _, name := range streamFiles {
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, forbid := range forbiddenIdents {
				if id.Name == forbid {
					t.Fatalf("%s must not reference %s (stream handlers stay off rating/journal/legacy money)", name, forbid)
				}
			}
			return true
		})
	}

	// Authority settle must not rebuild Rated money from stream CostPresent.
	lifecycleDir := dir
	lifecyclePaths, err := filepath.Glob(filepath.Join(lifecycleDir, "authority_lifecycle*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var lifecycleSrc []byte
	scanned := map[string]struct{}{}
	for _, lifecyclePath := range lifecyclePaths {
		if strings.HasSuffix(lifecyclePath, "_test.go") {
			continue
		}
		scanned[filepath.Base(lifecyclePath)] = struct{}{}
		src, err := os.ReadFile(lifecyclePath)
		if err != nil {
			t.Fatal(err)
		}
		lifecycleSrc = append(lifecycleSrc, src...)
		lifecycleSrc = append(lifecycleSrc, '\n')
	}
	if len(lifecycleSrc) == 0 {
		t.Fatal("authority_lifecycle sources not found")
	}
	for _, name := range []string{
		"authority_lifecycle.go",
		"authority_lifecycle_settle.go",
		"authority_lifecycle_release.go",
	} {
		if _, ok := scanned[name]; !ok {
			t.Fatalf("authority_lifecycle glob missed %s", name)
		}
	}
	body := string(lifecycleSrc)
	for _, term := range []string{
		"Rated:         rated",
		"Money:          moneyFromUsageEvent",
		"NanoUnits: usageEv.CostNanoUnits",
		"NanoUnits:     usageEv.CostNanoUnits",
	} {
		if strings.Contains(body, term) {
			t.Fatalf("authority_lifecycle must not feed stream cost into monetary Rated/Money settle (%q)", term)
		}
	}
}

// TestBillingSettlementRejectsRawUsageAndMeteringInputs locks design Fail-if
// "financial settlement consumes raw usage-event arrays or metering facts".
func TestBillingSettlementRejectsRawUsageAndMeteringInputs(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	paths := []string{
		filepath.Join(root, "internal", "core", "billing", "settlement.go"),
		filepath.Join(root, "internal", "core", "billing", "rating.go"),
		filepath.Join(root, "internal", "core", "billing", "call_post_usage_worker.go"),
	}
	forbidden := []string{
		"lipapi.Event",
		"[]lipapi.Event",
		"metering.Fact",
		"[]metering.Fact",
		"UsageDelta",
	}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(src)
		for _, term := range forbidden {
			if strings.Contains(body, term) {
				t.Fatalf("%s must not consume raw usage/metering financial inputs (%q)", filepath.Base(path), term)
			}
		}
	}
}

// TestPostTurnWorkerDoesNotMutateSealedTURPayload locks design Fail-if
// "sealed TUR/LUR payload is mutated for worker state".
func TestPostTurnWorkerDoesNotMutateSealedTURPayload(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "billing", "call_post_usage_worker.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenWrites := []string{
		"record.Legs",
		"record.SemanticFingerprint",
		"record.AccountID",
		"record.TurnID",
		"record.CustomerPricingRef",
		".Legs[",
	}
	body := string(src)
	for _, term := range forbiddenWrites {
		// Assignment forms only; method calls like record.SemanticFingerprint() are fingerprints, not mutation.
		if strings.Contains(body, term+" =") || strings.Contains(body, term+"=") {
			t.Fatalf("post_turn_worker must not mutate sealed TUR fields (%q)", term)
		}
	}
}

// TestNoSecondAuthoritativeCustomerBalanceReducerOutsideBillingStore locks
// design Fail-if "another authoritative customer-balance reducer appears".
func TestNoSecondAuthoritativeCustomerBalanceReducerOutsideBillingStore(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var offenders []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Domain billing may compute provisional Account values; durable
		// authoritative balance mutation stays in billingstore SQL/transactions.
		if strings.HasPrefix(rel, "internal/infra/billingstore/") || strings.HasPrefix(rel, "internal/core/billing/") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(src)
		if strings.Contains(body, "SET balance_nano") || strings.Contains(body, "balance_nano =") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("authoritative customer-balance mutation must stay in billingstore:\n%s", strings.Join(offenders, "\n"))
	}
}

// TestUsageRecordBillingFinalFlowPackagesExist locks Req 17.10 ownership surface:
// route/authorize/execute/TUR/rate/settle/journal/report packages remain present
// and directionally separated.
func TestUsageRecordBillingFinalFlowPackagesExist(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	required := []string{
		"internal/core/runtime",
		"internal/core/billing",
		"internal/infra/billingstore",
		"internal/infra/billingadmission",
		"internal/stdhttp/admin/billing",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("final billing flow package missing: %s (%v)", rel, err)
		}
	}
	assertDirectImportsExclude(t, "./internal/core/billing", "/internal/core/runtime",
		"billing must not import runtime (final flow keeps execute separate from settlement)")
	assertDirectImportsExclude(t, "./internal/core/runtime", "/internal/infra/billingstore",
		"runtime must not import billingstore (authorize+handoff only; journal stays post-turn)")
}
