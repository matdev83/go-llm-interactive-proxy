package openresponses

import (
	"testing"
)

// unsupportedRequestControls are official request controls the profile
// recognizes. The raw codec accepts their non-null wire shapes (they are valid
// JSON per the pinned schema); admission-time rejection of controls the
// canonical call cannot represent is a frontend concern, because the compact
// operation legitimately accepts a schema-permitted subset (instructions,
// prompt_cache_key). Metadata is deliberately excluded from rejection lists:
// the frontend maps it to Call.Session.Metadata.
var unsupportedRequestControls = []struct {
	field string
	value string // JSON value
}{
	{"instructions", `"Be brief"`},
	{"text", `{"format":"json_object"}`},
	{"reasoning", `{"effort":"low"}`},
	{"truncation", `"auto"`},
	{"service_tier", `"auto"`},
	{"safety_identifier", `"safety_001"`},
	{"prompt_cache_key", `"k1"`},
	{"prompt_cache_retention", `"recent"`},
	{"max_tool_calls", `5`},
}

func TestDecodeRequest_UnsupportedControlsRawDecodeAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range unsupportedRequestControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("raw codec must accept the pinned wire shape for %s=%s, got error: %v", tc.field, tc.value, err)
			}
			if !call.HasItemAuthority() || len(call.Items) != 1 {
				t.Fatalf("unexpected canonical call for %s=%s: %+v", tc.field, tc.value, call)
			}
		})
	}
}

func TestDecodeRequest_UnsupportedControlsNullAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range unsupportedRequestControls {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":null}`)
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("null %s must be accepted like an omitted field, got error: %v", tc.field, err)
			}
			if !call.HasItemAuthority() || len(call.Items) != 1 {
				t.Fatalf("unexpected canonical call for null %s: %+v", tc.field, call)
			}
			if len(call.Extensions) != 0 {
				t.Fatalf("null %s must not become an extension, got %v", tc.field, call.Extensions)
			}
		})
	}
}

func TestDecodeRequest_UnsupportedControlsMalformedFailDeterministically(t *testing.T) {
	t.Parallel()
	cases := []struct {
		field string
		value string
	}{
		{"instructions", `5`},
		{"truncation", `123`},
		{"service_tier", `true`},
		{"max_tool_calls", `"five"`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)
			if _, _, err := DecodeRequest(body); err == nil {
				t.Fatalf("malformed %s=%s must fail, got nil", tc.field, tc.value)
			}
		})
	}
}

func TestDecodeRequest_SupportedControlsPreserved(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"model":"gpt-4o",
		"input":"hello",
		"temperature":0.7,
		"top_p":0.9,
		"max_output_tokens":256,
		"parallel_tool_calls":true,
		"metadata":{"tenant":"acme"}
	}`)
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("supported controls must decode: %v", err)
	}
	if call.Options.Temperature == nil || *call.Options.Temperature != 0.7 {
		t.Fatalf("temperature=%v, want 0.7", call.Options.Temperature)
	}
	if call.Options.TopP == nil || *call.Options.TopP != 0.9 {
		t.Fatalf("top_p=%v, want 0.9", call.Options.TopP)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 256 {
		t.Fatalf("max_output_tokens=%v, want 256", call.Options.MaxOutputTokens)
	}
	if call.Options.ParallelToolCalls == nil || !*call.Options.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls=%v, want true", call.Options.ParallelToolCalls)
	}
}
