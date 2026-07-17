package toolcallrepair_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair"
)

func TestValidate_InstancePath(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"loc":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}
		},
		"required":["loc"]
	}`)
	err := toolcallrepair.ValidateArgsAgainstSchema([]byte(`{"loc":{"city":1}}`), schema)
	var se *toolcallrepair.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("got %v", err)
	}
	if se.Kind != toolcallrepair.SchemaKindValidationFailed {
		t.Fatalf("kind=%q", se.Kind)
	}
	if se.InstancePath != "/loc/city" {
		t.Fatalf("InstancePath=%q want /loc/city", se.InstancePath)
	}
}

func TestValidate_ErrorOmitsPayloads(t *testing.T) {
	t.Parallel()
	secret := "super-secret-token-value"
	schema := json.RawMessage(`{"type":"object","properties":{"token":{"type":"number"}},"required":["token"]}`)
	args := []byte(`{"token":"` + secret + `"}`)
	err := toolcallrepair.ValidateArgsAgainstSchema(args, schema)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("error leaked args payload: %q", msg)
	}
	if strings.Contains(msg, `"type":"object"`) || strings.Contains(msg, "properties") {
		t.Fatalf("error leaked schema payload: %q", msg)
	}
	if !strings.Contains(msg, toolcallrepair.SchemaKindValidationFailed) {
		t.Fatalf("error missing kind: %q", msg)
	}
}

func TestValidate_JSONNumberPrecision(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","const":9007199254740993}},"required":["n"]}`)
	if err := toolcallrepair.ValidateArgsAgainstSchema([]byte(`{"n":9007199254740993}`), schema); err != nil {
		t.Fatalf("json.Number precision path failed: %v", err)
	}
	err := toolcallrepair.ValidateArgsAgainstSchema([]byte(`{"n":9007199254740994}`), schema)
	if err == nil {
		t.Fatal("expected const mismatch")
	}
}

func TestValidate_CompiledSchemaConcurrent(t *testing.T) {
	t.Parallel()
	cache := toolcallrepair.NewSchemaCache(toolcallrepair.DefaultSchemaLimits())
	cs, err := cache.GetOrCompile(json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	const n = 16
	errs := make(chan error, n)
	for range n {
		go func() {
			errs <- cs.Validate([]byte(`{"x":"ok"}`))
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent validate: %v", err)
		}
	}
}

func TestValidate_CanceledContext(t *testing.T) {
	t.Parallel()
	cache := toolcallrepair.NewSchemaCache(toolcallrepair.DefaultSchemaLimits())
	cs, err := cache.GetOrCompile(json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = cs.ValidateContext(ctx, []byte(`{"x":"ok"}`))
	var se *toolcallrepair.SchemaError
	if !errors.As(err, &se) || se.ReasonCode != toolcallrepair.ReasonCanceled {
		t.Fatalf("want canceled, got %v", err)
	}
}
