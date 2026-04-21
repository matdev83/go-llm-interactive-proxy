package refsubmit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestSubmitHook_setsExtension(t *testing.T) {
	t.Parallel()
	h := NewHook(Config{Marker: "probe"})
	call := &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}}}
	_, err := h.Handle(context.Background(), call, &sdk.SubmitMeta{TraceID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := call.Extensions["x_lip_ref_submit"]
	if !ok {
		t.Fatal("missing extension")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s != "probe" {
		t.Fatalf("value %q err %v", string(raw), err)
	}
}
