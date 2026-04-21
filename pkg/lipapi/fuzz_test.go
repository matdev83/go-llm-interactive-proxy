package lipapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzCallValidateJSON(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"Messages":[{"Role":"user","Parts":[{"Kind":"text","Text":"x"}]}]}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 512<<10)
		var c lipapi.Call
		if err := json.Unmarshal(raw, &c); err != nil {
			return
		}
		_ = c.Validate()
	})
}

func FuzzMergeRouteQueryGenerationOptions(f *testing.F) {
	f.Add("temperature=0.5&max_output_tokens=3")
	f.Add("parallel_tool_calls=1")

	f.Fuzz(func(t *testing.T, q string) {
		q = testkit.CapString(q, 16<<10)
		v, err := url.ParseQuery(q)
		if err != nil {
			return
		}
		_, _ = lipapi.MergeRouteQueryIntoGenerationOptions(lipapi.GenerationOptions{}, v)
	})
}

func FuzzCollectWithLimitsProgram(f *testing.F) {
	f.Add([]byte{1, 2, 3})

	f.Fuzz(func(t *testing.T, b []byte) {
		b = testkit.CapBytes(b, 4096)
		evs := collectFuzzEvents(b)
		ctx := context.Background()
		_, _ = lipapi.CollectWithLimits(ctx, lipapi.FixedEventStream(evs), lipapi.CollectLimits{
			MaxTextBytes:          1 << 20,
			MaxReasoningBytes:     1 << 20,
			MaxToolArgsTotalBytes: 1 << 20,
			MaxWarnings:           1000,
		})
	})
}

func collectFuzzEvents(b []byte) []lipapi.Event {
	evs := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
	}
	const chunk = 48
	for i := 0; i < len(b); i += chunk {
		j := i + chunk
		if j > len(b) {
			j = len(b)
		}
		if i >= j {
			break
		}
		kind := lipapi.EventTextDelta
		if b[i]%2 == 0 {
			kind = lipapi.EventReasoningDelta
		}
		evs = append(evs, lipapi.Event{Kind: kind, Delta: string(b[i:j])})
	}
	evs = append(evs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return evs
}
