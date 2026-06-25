package openaicodex

import (
	"encoding/json"
	"testing"
)

func TestJSONRawString(t *testing.T) {
	t.Parallel()
	root := map[string]json.RawMessage{
		"access_token": json.RawMessage(`"snake-val"`),
		"accessToken":  json.RawMessage(`"camel-val"`),
		"empty":        json.RawMessage(`""`),
		"bad":          json.RawMessage(`123`),
	}
	if got := jsonRawString(root, "missing"); got != "" {
		t.Fatalf("missing: %q", got)
	}
	if got := jsonRawString(root, "access_token", "accessToken"); got != "snake-val" {
		t.Fatalf("snake first: %q", got)
	}
	if got := jsonRawString(root, "accessToken", "access_token"); got != "camel-val" {
		t.Fatalf("camel first: %q", got)
	}
	if got := jsonRawString(root, "empty"); got != "" {
		t.Fatalf("empty: %q", got)
	}
	if got := jsonRawString(root, "bad"); got != "" {
		t.Fatalf("bad type: %q", got)
	}
}
