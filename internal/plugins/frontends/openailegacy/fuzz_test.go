package openailegacy_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func FuzzDecodeChatRequest(f *testing.F) {
	f.Add([]byte{0, '{', '}'})
	f.Add(append([]byte{0}, []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}]}`)...))

	f.Fuzz(func(t *testing.T, packed []byte) {
		packed = testkit.CapBytes(packed, 256<<10)
		sel, body := testkit.SplitFuzzPackedRouteBody(packed, 64<<10, 256<<10)
		_, _ = openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: sel})
	})
}
