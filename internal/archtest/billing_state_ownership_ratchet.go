package archtest

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
)

// EvaluateBillingCallScopedStateOwnership rejects executor-global
// lifetime-growing billing-call registries and requires call-scoped state to
// live on request/stream objects, not on the executor. Ordinary provider maps
// such as a string-keyed Backends catalog and request-scoped bookkeeping (for
// example a per-stream recorded-leg set) are not flagged.
func EvaluateBillingCallScopedStateOwnership(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	out = append(out, scanRuntimeLifetimeRegistryIdents(root)...)
	out = append(out, scanExecutorStructBillingFields(root)...)
	out = append(out, scanBillingCallIDKeyedMaps(root)...)
	out = append(out, scanCallScopedStateOwners(root)...)
	return out, nil
}

// scanRuntimeLifetimeRegistryIdents forbids the removed executor-global
// collector names anywhere in runtime production.
func scanRuntimeLifetimeRegistryIdents(root string) []RuleFinding {
	rel := "internal/core/runtime"
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []RuleFinding
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		fileRel := rel + "/" + entry.Name()
		src, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		body := string(src)
		for _, name := range billingCorrectnessLifetimeRegistryIdents {
			if strings.Contains(body, name) {
				out = append(out, billingCorrectnessRuleFinding(
					BillingCorrectnessRuleExecutorGlobalBillingRegistry, fileRel,
					"executor-global lifetime billing registry "+name+" must not return"))
			}
		}
	}
	return out
}

// scanExecutorStructBillingFields forbids billing-call registries declared
// directly on the Executor struct: lifetime-registry names, BillingCallID-keyed
// maps, and call-scoped state fields. Legitimate maps such as a string-keyed
// Backends catalog are allowed.
func scanExecutorStructBillingFields(root string) []RuleFinding {
	rel := "internal/core/runtime/executor.go"
	f, err := parseProductionFile(root, rel)
	if err != nil || f == nil {
		return nil
	}
	ts := findTypeSpec(f, "Executor")
	if ts == nil {
		return nil
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return nil
	}
	var out []RuleFinding
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			continue // embedded type
		}
		for _, name := range field.Names {
			if name.Name == "billingCallState" || name.Name == "billingTurnCollector" {
				out = append(out, billingCorrectnessRuleFinding(
					BillingCorrectnessRuleExecutorGlobalBillingRegistry, rel,
					"call-scoped billing state must not live on the executor ("+name.Name+")"))
			}
			if isBillingRegistryFieldName(name.Name) {
				out = append(out, billingCorrectnessRuleFinding(
					BillingCorrectnessRuleExecutorGlobalBillingRegistry, rel,
					"executor-global billing registry field "+name.Name+" is forbidden"))
			}
		}
		if isBillingCallIDKeyedMap(field) {
			out = append(out, billingCorrectnessRuleFinding(
				BillingCorrectnessRuleExecutorMapField, rel,
				"executor may not hold a BillingCallID-keyed map (lifetime-growing call registry)"))
		}
	}
	return out
}

func isBillingRegistryFieldName(name string) bool {
	for _, suffix := range []string{"ByCall", "ByBillingCall", "Collector"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func isBillingCallIDKeyedMap(field *ast.Field) bool {
	mt, ok := field.Type.(*ast.MapType)
	if !ok {
		return false
	}
	return isBillingCallIDKeyExpr(mt.Key)
}

// scanBillingCallIDKeyedMaps rejects any production BillingCallID-keyed map
// registry under runtime (executor-global or otherwise).
func scanBillingCallIDKeyedMaps(root string) []RuleFinding {
	rel := "internal/core/runtime"
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []RuleFinding
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parseProductionFile(root, rel+"/"+entry.Name())
		if err != nil || f == nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			mt, ok := n.(*ast.MapType)
			if !ok {
				return true
			}
			if isBillingCallIDKeyExpr(mt.Key) {
				out = append(out, billingCorrectnessRuleFinding(
					BillingCorrectnessRuleExecutorMapField, rel+"/"+entry.Name(),
					"runtime must not hold a BillingCallID-keyed map registry (request-scoped state replaces it)"))
			}
			return true
		})
	}
	return out
}

// scanCallScopedStateOwners requires the request-scoped owner structs to carry
// the private billingCallState object.
func scanCallScopedStateOwners(root string) []RuleFinding {
	var out []RuleFinding
	for _, require := range []struct {
		rel       string
		typeName  string
		fieldName string
	}{
		{rel: "internal/core/runtime/executor_prepare_request.go", typeName: "preparedRequest", fieldName: "billingCallID"},
		{rel: "internal/core/runtime/executor_prepare_request.go", typeName: "preparedRequest", fieldName: "billingCallState"},
		{rel: "internal/core/runtime/executor_retry_stream.go", typeName: "retryRecvStream", fieldName: "billingCallState"},
	} {
		f, err := parseProductionFile(root, require.rel)
		if err != nil || f == nil {
			continue
		}
		ts := findTypeSpec(f, require.typeName)
		if ts == nil {
			out = append(out, billingCorrectnessRuleFinding(
				BillingCorrectnessRuleCallScopedStateOwnerMissing, require.rel,
				"request-scoped owner type "+require.typeName+" is missing"))
			continue
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || !structHasField(st, require.fieldName) {
			out = append(out, billingCorrectnessRuleFinding(
				BillingCorrectnessRuleCallScopedStateOwnerMissing, require.rel,
				require.typeName+" must own the private "+require.fieldName+" field"))
		}
	}
	return out
}

func structHasField(st *ast.StructType, name string) bool {
	for _, field := range st.Fields.List {
		for _, n := range field.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}
