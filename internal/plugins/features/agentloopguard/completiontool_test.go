package agentloopguard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
)

func TestAttemptCompletionSpecIsPinned(t *testing.T) {
	p := NewControlProvider()
	id, spec, err := controltool.Resolve(p)
	if err != nil { t.Fatal(err) }
	if id != controlProviderID || spec.Tool.Name != "attempt_completion" || spec.Tool.Description != attemptCompletionDescription {
		t.Fatalf("id=%q spec=%+v", id, spec)
	}
	if string(spec.Tool.Parameters) != attemptCompletionSchema {
		t.Fatalf("schema=%s", spec.Tool.Parameters)
	}
	if spec.Instruction.Text != AttemptCompletionInstruction || strings.Contains(string(spec.Tool.Parameters), "command") {
		t.Fatal("pinned completion protocol changed")
	}
	var schema map[string]any
	if json.Unmarshal(spec.Tool.Parameters, &schema) != nil { t.Fatal("schema invalid") }
}

func TestAttemptCompletionHandlerStrictArgs(t *testing.T) {
	p := NewControlProvider()
	cases := []struct{ raw string; complete bool }{
		{`{"result":"done"}`, true},
		{`{"result":"  done  "}`, true},
		{`{}`, false},
		{`{"result":""}`, false},
		{`{"result":1}`, false},
		{`{"result":"a","result":"b"}`, false},
		{`{"result":"a","extra":true}`, false},
		{`{"result":"a"} {"x":1}`, false},
		{`["result","a"]`, false},
	}
	for _, tc := range cases {
		out, err := p.Handle(context.Background(), controltool.CompletedCall{ToolCallID:"c",ToolName:"attempt_completion",ArgsJSON:[]byte(tc.raw)}, controltool.Meta{})
		if err != nil { t.Fatal(err) }
		if (out.Kind == controltool.OutcomeComplete) != tc.complete {
			t.Fatalf("raw=%q outcome=%+v", tc.raw, out)
		}
		if err := controltool.ValidateOutcome(out); err != nil { t.Fatalf("raw=%q validation=%v", tc.raw, err) }
	}
}
