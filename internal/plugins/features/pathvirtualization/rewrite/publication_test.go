package rewrite_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRewriteCall_PublicationPreservesAuditAndEnvelopeBytes(t *testing.T) {
	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatal(reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatal(reject)
	}
	args := `{"file_path":"` + fixtureTarget + `"}`
	wrapped, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	envelope := " { \"name\" : \"read_file\", \"arguments\" : " + string(wrapped) + ",\n \"call_id\" : \"call_7f3a\", \"type\" : \"function_call\", \"id\" : \"\\u0061\" } "
	for _, tc := range []struct {
		name     string
		call     *lipapi.Call
		payload  func(*lipapi.Call) string
		rewrites bool
	}{
		{"raw_item", &lipapi.Call{Items: []lipapi.Item{toolCallItem("read_file", args)}}, func(c *lipapi.Call) string { return string(c.Items[0].ToolCall.Arguments) }, true},
		{"wrapped_item", &lipapi.Call{Items: []lipapi.Item{toolCallItem("read_file", string(wrapped))}}, func(c *lipapi.Call) string { return string(c.Items[0].ToolCall.Arguments) }, true},
		{"named_part", &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartJSON, ToolName: "read_file", ToolCallID: "call_7f3a", Content: []byte(args)}}}}}, func(c *lipapi.Call) string { return string(c.Messages[0].Parts[0].Content) }, true},
		{"envelope", &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartJSON, Content: []byte(envelope)}}}}}, func(c *lipapi.Call) string { return string(c.Messages[0].Parts[0].Content) }, true},
		{"object_envelope", &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartJSON, Content: []byte(strings.Replace(envelope, string(wrapped), args, 1))}}}}}, func(c *lipapi.Call) string { return string(c.Messages[0].Parts[0].Content) }, false},
	} {
		for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
			t.Run(tc.name+"/"+mode.String(), func(t *testing.T) {
				before := tc.payload(tc.call)
				out, stats, err := rewrite.NewWithMode(fixtureMapping(t), resolver, mode).RewriteCall(tc.call)
				if err != nil {
					t.Fatal(err)
				}
				want := before
				if mode == rewrite.ModeRewrite && tc.rewrites {
					want = strings.Replace(before, fixtureTarget, fixtureVPath, 1)
				}
				if got := tc.payload(out); got != want {
					t.Errorf("payload:\n got %s\nwant %s", got, want)
				}
				if tc.payload(tc.call) != before {
					t.Error("input call was mutated")
				}
				if tc.rewrites && stats.Rewritten != 1 {
					t.Errorf("detected replacements = %d, want 1", stats.Rewritten)
				}
			})
		}
	}
}
