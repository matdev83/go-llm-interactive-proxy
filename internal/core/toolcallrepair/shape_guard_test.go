package toolcallrepair_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

func nestedObjectJSON(depth int) []byte {
	var b strings.Builder
	for range depth {
		b.WriteString(`{"a":`)
	}
	b.WriteByte('1')
	for range depth {
		b.WriteByte('}')
	}
	return []byte(b.String())
}

func TestSchemaDefaultDepthBoundaries(t *testing.T) {
	t.Parallel()
	limits := toolcallrepair.DefaultSchemaLimits()
	if limits.MaxNestingDepth != 32 {
		t.Fatalf("DefaultSchemaLimits.MaxNestingDepth=%d want 32", limits.MaxNestingDepth)
	}
	cache := toolcallrepair.NewSchemaCache(limits)
	if _, err := cache.GetOrCompile(nestedObjectJSON(32)); err != nil {
		t.Fatalf("depth exact 32 compile: %v", err)
	}
	err := mustSchemaErr(t, func() error {
		_, e := cache.GetOrCompile(nestedObjectJSON(33))
		return e
	})
	assertSchemaReason(t, err, toolcallrepair.ReasonNestingTooDeep)
	assertNoSecret(t, err.Error(), `"a":`)
}

func TestRepairArgsJSONPreflightsBeforeParse(t *testing.T) {
	t.Parallel()
	schemaOK := json.RawMessage(`{"type":"object"}`)
	argsOver := []byte(strings.Repeat(`[`, jsonshape.ToolArgumentsLimits().MaxDepth+1) + `1` + strings.Repeat(`]`, jsonshape.ToolArgumentsLimits().MaxDepth+1))
	_, reason, err := toolcallrepair.ExportRepairArgsJSON(context.Background(), argsOver, schemaOK, toolcallrepair.DefaultMaxArgsBytes, toolcallrepair.DefaultSchemaLimits())
	if err == nil {
		t.Fatal("expected args depth+1 to fail before parse")
	}
	if reason != "" {
		t.Fatalf("reason=%q want empty on hard fail", reason)
	}
	if !strings.Contains(err.Error(), toolcall.ReasonUnrepairable) {
		t.Fatalf("err=%v want reason %q", err, toolcall.ReasonUnrepairable)
	}
	assertNoSecret(t, err.Error(), "1")

	schemaOver := nestedObjectJSON(toolcallrepair.DefaultSchemaLimits().MaxNestingDepth + 1)
	_, _, err = toolcallrepair.ExportRepairArgsJSON(context.Background(), []byte(`{}`), schemaOver, toolcallrepair.DefaultMaxArgsBytes, toolcallrepair.DefaultSchemaLimits())
	if err == nil {
		t.Fatal("expected schema depth+1 to fail before parse")
	}
	if !strings.Contains(err.Error(), toolcall.ReasonSchemaInvalid) {
		t.Fatalf("err=%v want reason %q", err, toolcall.ReasonSchemaInvalid)
	}
	assertNoSecret(t, err.Error(), `"a":`)
}

func TestRepairArgsJSONHonorsCustomPolicyAndCancel(t *testing.T) {
	t.Parallel()

	t.Run("max_args_over_default", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"x":"` + strings.Repeat("a", 70<<10) + `"}`)
		if len(payload) <= toolcallrepair.DefaultMaxArgsBytes {
			t.Fatalf("fixture too small: %d", len(payload))
		}
		_, _, err := toolcallrepair.ExportRepairArgsJSON(context.Background(), payload, json.RawMessage(`{"type":"object"}`), toolcallrepair.DefaultMaxArgsBytes, toolcallrepair.DefaultSchemaLimits())
		if err == nil {
			t.Fatal("default max must reject oversized args")
		}
		_, _, err = toolcallrepair.ExportRepairArgsJSON(context.Background(), payload, json.RawMessage(`{"type":"object"}`), 100<<10, toolcallrepair.DefaultSchemaLimits())
		if err != nil {
			t.Fatalf("custom maxArgsBytes must accept: %v", err)
		}
	})

	t.Run("schema_depth_over_default", func(t *testing.T) {
		t.Parallel()
		schema := nestedObjectJSON(35)
		_, _, err := toolcallrepair.ExportRepairArgsJSON(context.Background(), []byte(`{}`), schema, toolcallrepair.DefaultMaxArgsBytes, toolcallrepair.DefaultSchemaLimits())
		if err == nil {
			t.Fatal("default schema depth must reject depth 35")
		}
		limits := toolcallrepair.DefaultSchemaLimits()
		limits.MaxNestingDepth = 40
		_, _, err = toolcallrepair.ExportRepairArgsJSON(context.Background(), []byte(`{}`), schema, toolcallrepair.DefaultMaxArgsBytes, limits)
		if err != nil {
			t.Fatalf("custom schema depth must accept: %v", err)
		}
	})

	t.Run("canceled_before_parse", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := toolcallrepair.ExportRepairArgsJSON(ctx, []byte(`{}`), json.RawMessage(`{"type":"object"}`), toolcallrepair.DefaultMaxArgsBytes, toolcallrepair.DefaultSchemaLimits())
		if err == nil {
			t.Fatal("expected canceled")
		}
		if !strings.Contains(err.Error(), toolcall.ReasonCanceled) {
			t.Fatalf("err=%v want %q", err, toolcall.ReasonCanceled)
		}
		assertNoSecret(t, err.Error(), "object")
	})
}

func TestEngineRepairHonorsCacheLimitsAndLargeMaxArgs(t *testing.T) {
	t.Parallel()

	t.Run("schema_cache_limits_snapshot", func(t *testing.T) {
		t.Parallel()
		limits := toolcallrepair.DefaultSchemaLimits()
		limits.MaxNestingDepth = 40
		cache := toolcallrepair.NewSchemaCache(limits)
		got := cache.Limits()
		if got.MaxNestingDepth != 40 {
			t.Fatalf("Limits().MaxNestingDepth=%d want 40", got.MaxNestingDepth)
		}
		got.MaxNestingDepth = 1
		if cache.Limits().MaxNestingDepth != 40 {
			t.Fatal("Limits() must return a defensive copy")
		}
	})

	t.Run("engine_large_max_args_empty_schema", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"x":"` + strings.Repeat("b", 70<<10) + `"}`)
		eng := toolcallrepair.NewEngine()
		out, err := eng.RepairContext(context.Background(), toolcallrepair.Input{
			ToolName:     "run",
			ArgsJSON:     payload,
			Catalog:      []lipapi.ToolDef{{Name: "run", Parameters: nil}},
			MaxArgsBytes: 100 << 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Kind != toolcallrepair.OutcomePass {
			t.Fatalf("kind=%v reason=%q", out.Kind, out.ReasonCode)
		}
	})

	t.Run("engine_custom_schema_depth_repair", func(t *testing.T) {
		t.Parallel()
		limits := toolcallrepair.DefaultSchemaLimits()
		limits.MaxNestingDepth = 40
		cache := toolcallrepair.NewSchemaCache(limits)
		eng := toolcallrepair.NewEngineWithCache(cache)
		schema := nestedObjectJSON(35)
		out, err := eng.RepairContext(context.Background(), toolcallrepair.Input{
			ToolName: "run",
			ArgsJSON: []byte(`{}`),
			Catalog:  []lipapi.ToolDef{{Name: "run", Parameters: schema}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Kind != toolcallrepair.OutcomePass {
			t.Fatalf("kind=%v reason=%q want pass under custom schema depth", out.Kind, out.ReasonCode)
		}
	})
}

func TestSchemaPreflightRejectsBeforeMaterialize(t *testing.T) {
	t.Parallel()

	limits := toolcallrepair.DefaultSchemaLimits()
	limits.MaxNestingDepth = 3
	limits.MaxProperties = 2
	cache := toolcallrepair.NewSchemaCache(limits)

	deepOK := []byte(`{"a":{"b":{"c":1}}}`)
	if _, err := cache.GetOrCompile(deepOK); err != nil {
		t.Fatalf("depth exact compile: %v", err)
	}
	deepOver := []byte(`{"a":{"b":{"c":{"d":1}}}}`)
	err := mustSchemaErr(t, func() error {
		_, e := cache.GetOrCompile(deepOver)
		return e
	})
	assertSchemaReason(t, err, toolcallrepair.ReasonNestingTooDeep)

	dup := []byte(`{"type":"object","type":"string"}`)
	err = mustSchemaErr(t, func() error {
		_, e := cache.GetOrCompile(dup)
		return e
	})
	assertSchemaReason(t, err, toolcallrepair.ReasonMalformedJSON)

	wide := []byte(`{"a":1,"b":2,"c":3}`)
	err = mustSchemaErr(t, func() error {
		_, e := cache.GetOrCompile(wide)
		return e
	})
	assertSchemaReason(t, err, toolcallrepair.ReasonTooManyProperties)
}

func TestSchemaShapeLimitsArrayCapFollowsMaxNodes(t *testing.T) {
	t.Parallel()
	limits := toolcallrepair.DefaultSchemaLimits()
	limits.MaxNodes = 10
	maxArr, maxNodes, _ := toolcallrepair.ExportSchemaShapeLimits(limits)
	if maxArr != 10 || maxNodes != 10 {
		t.Fatalf("MaxArrayElems=%d MaxNodes=%d want both 10", maxArr, maxNodes)
	}
	if maxArr > maxNodes {
		t.Fatal("MaxArrayElems must never exceed MaxNodes")
	}
}

func TestSchemaPreflightFlatArrayExceedingMaxNodes(t *testing.T) {
	t.Parallel()
	limits := toolcallrepair.DefaultSchemaLimits()
	limits.MaxNodes = 10
	schema := []byte(`[` + strings.TrimRight(strings.Repeat(`1,`, 20), ",") + `]`)
	err := toolcallrepair.ExportPreflightSchemaJSON(context.Background(), schema, limits)
	assertSchemaReason(t, err, toolcallrepair.ReasonTooManyNodes)
	assertNoSecret(t, err.Error(), "1,1")

	err = mustSchemaErr(t, func() error {
		_, e := toolcallrepair.NewSchemaCache(limits).GetOrCompile(schema)
		return e
	})
	assertSchemaReason(t, err, toolcallrepair.ReasonTooManyNodes)
}

func TestMapEngineSchemaReasonSafeSpecific(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "canceled", err: &toolcallrepair.SchemaError{Kind: toolcallrepair.SchemaKindInvalid, ReasonCode: toolcallrepair.ReasonCanceled}, want: toolcall.ReasonCanceled},
		{name: "unsupported", err: &toolcallrepair.SchemaError{Kind: toolcallrepair.SchemaKindUnsupported, ReasonCode: toolcall.ReasonSchemaUnsupported}, want: toolcall.ReasonSchemaUnsupported},
		{name: "too_many_nodes", err: &toolcallrepair.SchemaError{Kind: toolcallrepair.SchemaKindLimitExceeded, ReasonCode: toolcallrepair.ReasonTooManyNodes}, want: toolcall.ReasonSchemaInvalid},
		{name: "nesting", err: &toolcallrepair.SchemaError{Kind: toolcallrepair.SchemaKindLimitExceeded, ReasonCode: toolcallrepair.ReasonNestingTooDeep}, want: toolcall.ReasonSchemaInvalid},
		{name: "malformed", err: &toolcallrepair.SchemaError{Kind: toolcallrepair.SchemaKindMalformed, ReasonCode: toolcallrepair.ReasonMalformedJSON}, want: toolcall.ReasonSchemaInvalid},
		{name: "plain", err: errors.New("boom"), want: toolcall.ReasonSchemaInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := toolcallrepair.ExportMapEngineSchemaReason(tc.err); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSchemaPreflightCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := toolcallrepair.NewSchemaCache(toolcallrepair.DefaultSchemaLimits()).GetOrCompileContext(ctx, json.RawMessage(`{"type":"object"}`))
	assertSchemaReason(t, err, toolcallrepair.ReasonCanceled)
	assertNoSecret(t, err.Error(), "object")
}

func TestValidateContextShapeAndCancel(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`)
	cs, err := toolcallrepair.NewSchemaCache(toolcallrepair.DefaultSchemaLimits()).GetOrCompile(schema)
	if err != nil {
		t.Fatal(err)
	}

	depthOver := jsonshape.ToolArgumentsLimits().MaxDepth + 1
	deep := []byte(strings.Repeat(`[`, depthOver) + `1` + strings.Repeat(`]`, depthOver))
	err = cs.ValidateContext(context.Background(), deep)
	assertSchemaReason(t, err, toolcallrepair.ReasonNestingTooDeep)
	assertNoSecret(t, err.Error(), "1")

	dup := []byte(`{"n":1,"n":2}`)
	err = cs.ValidateContext(context.Background(), dup)
	assertSchemaReason(t, err, toolcallrepair.ReasonMalformedJSON)

	num := []byte(`{"n":` + strings.Repeat("9", 80) + `}`)
	err = cs.ValidateContext(context.Background(), num)
	assertSchemaReason(t, err, toolcallrepair.ReasonArgsTooLargeShape)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = cs.ValidateContext(ctx, []byte(`{"n":1}`))
	assertSchemaReason(t, err, toolcallrepair.ReasonCanceled)
}

func TestEngineRepairContextShapeGuards(t *testing.T) {
	t.Parallel()
	catalog := []lipapi.ToolDef{{
		Name:       "run",
		Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`),
	}}
	eng := toolcallrepair.NewEngineWithCache(toolcallrepair.NewSchemaCache(toolcallrepair.DefaultSchemaLimits()))

	depthLimit := jsonshape.ToolArgumentsLimits().MaxDepth
	emptyCatalog := []lipapi.ToolDef{{Name: "run", Parameters: nil}}
	at := []byte(strings.Repeat(`[`, depthLimit) + `1` + strings.Repeat(`]`, depthLimit))
	out, err := eng.RepairContext(context.Background(), toolcallrepair.Input{
		ToolName: "run", ArgsJSON: at, Catalog: emptyCatalog, MaxArgsBytes: 64 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind == toolcallrepair.OutcomeUnrepairable {
		t.Fatalf("exact depth=%d must not be Unrepairable (kind=%v reason=%q)", depthLimit, out.Kind, out.ReasonCode)
	}
	if out.Kind != toolcallrepair.OutcomePass && out.Kind != toolcallrepair.OutcomeRewrite {
		t.Fatalf("exact depth kind=%v want Pass or Rewrite", out.Kind)
	}

	over := []byte(strings.Repeat(`[`, depthLimit+1) + `1` + strings.Repeat(`]`, depthLimit+1))
	out, err = eng.RepairContext(context.Background(), toolcallrepair.Input{
		ToolName: "run", ArgsJSON: over, Catalog: emptyCatalog, MaxArgsBytes: 64 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != toolcallrepair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonUnrepairable {
		t.Fatalf("depth+1: kind=%v reason=%q", out.Kind, out.ReasonCode)
	}
	if string(out.ArgsJSON) != string(over) {
		t.Fatal("original args not preserved")
	}

	cases := []struct {
		name   string
		args   []byte
		reason string
	}{
		{name: "duplicate", args: []byte(`{"n":1,"n":2}`), reason: toolcall.ReasonUnrepairable},
		{name: "invalid_utf8", args: []byte{'{', '"', 'n', '"', ':', '"', 0xff, '"', '}'}, reason: toolcall.ReasonUnrepairable},
		{name: "huge_number", args: []byte(`{"n":` + strings.Repeat("9", 80) + `}`), reason: toolcall.ReasonArgsTooLarge},
		{name: "wide_array", args: []byte(`[` + strings.TrimRight(strings.Repeat(`1,`, 5000), ",") + `]`), reason: toolcall.ReasonUnrepairable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := eng.RepairContext(context.Background(), toolcallrepair.Input{
				ToolName: "run", ArgsJSON: tc.args, Catalog: catalog, MaxArgsBytes: 64 << 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != toolcallrepair.OutcomeUnrepairable || out.ReasonCode != tc.reason {
				t.Fatalf("kind=%v reason=%q want %q", out.Kind, out.ReasonCode, tc.reason)
			}
			if string(out.ArgsJSON) != string(tc.args) {
				t.Fatal("original not preserved")
			}
		})
	}
}

func TestEngineRepairContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := toolcallrepair.NewEngine().RepairContext(ctx, toolcallrepair.Input{
		ToolName: "run",
		ArgsJSON: []byte(`{"n":1}`),
		Catalog:  []lipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != toolcallrepair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonCanceled {
		t.Fatalf("kind=%v reason=%q", out.Kind, out.ReasonCode)
	}
}

func TestEngineEmptySchemaStillShapeChecks(t *testing.T) {
	t.Parallel()
	eng := toolcallrepair.NewEngine()
	depthOver := jsonshape.ToolArgumentsLimits().MaxDepth + 1
	over := []byte(strings.Repeat(`[`, depthOver) + `0` + strings.Repeat(`]`, depthOver))
	out, err := eng.RepairContext(context.Background(), toolcallrepair.Input{
		ToolName: "run",
		ArgsJSON: over,
		Catalog:  []lipapi.ToolDef{{Name: "run", Parameters: nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != toolcallrepair.OutcomeUnrepairable {
		t.Fatalf("kind=%v want unrepairable", out.Kind)
	}
}

func TestFinalizerPropagatesCancelReason(t *testing.T) {
	t.Parallel()
	fin := toolcallrepair.NewFinalizer(toolcallrepair.FinalizerPolicy{
		MaxArgsBytes:   1024,
		OnUnrepairable: toolcallrepair.OnUnrepairableError,
		Order:          toolcallrepair.DefaultFinalizerOrder,
		Schema:         toolcallrepair.DefaultSchemaLimits(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := fin.Finalize(ctx, toolcall.CompletedCall{
		ToolCallID: "c1", ToolName: "run", ArgsJSON: []byte(`{"n":1}`),
	}, lipapi.ToolDef{Name: "run"}, []lipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(`{"type":"object"}`)}}, toolcall.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonCanceled {
		t.Fatalf("action=%v reason=%q", res.Action, res.ReasonCode)
	}
}

func TestEngineCompletedSyntaxCandidateIsPreflighted(t *testing.T) {
	t.Parallel()
	prefix := strings.Repeat(`[`, jsonshape.ToolArgumentsLimits().MaxDepth+1)
	eng := toolcallrepair.NewEngine()
	out, err := eng.RepairContext(context.Background(), toolcallrepair.Input{
		ToolName: "run",
		ArgsJSON: []byte(prefix),
		Catalog:  []lipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(`null`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != toolcallrepair.OutcomeUnrepairable {
		t.Fatalf("kind=%v want unrepairable after completed deep JSON", out.Kind)
	}
}

func mustSchemaErr(t *testing.T, fn func() error) error {
	t.Helper()
	err := fn()
	if err == nil {
		t.Fatal("expected error")
	}
	return err
}

func assertSchemaReason(t *testing.T, err error, want string) {
	t.Helper()
	var se *toolcallrepair.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("got %T (%v), want *SchemaError", err, err)
	}
	if se.ReasonCode != want {
		t.Fatalf("reason=%q want %q (err=%v)", se.ReasonCode, want, err)
	}
}
