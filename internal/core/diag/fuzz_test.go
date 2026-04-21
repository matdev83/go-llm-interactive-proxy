package diag_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzStableCallIdentity(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"ID":"x","Messages":[{"Role":"user","Parts":[{"Kind":"text","Text":"y"}]}]}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 512<<10)
		var c lipapi.Call
		if err := json.Unmarshal(raw, &c); err != nil {
			c = lipapi.Call{}
		}
		_ = diag.StableCallToken(&c)
		_ = diag.StableCallID(&c)
		_ = diag.StableUnix(&c)
	})
}
