package cursorsdk

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapBridgeEvent_RedactsAPIKeyAndKeyishPatterns(t *testing.T) {
	t.Parallel()
	apiKey := "secret-api-key-value-for-mapper"
	sk := "sk-abcdefghijklmnopqrstuvwx"
	crsr := "crsr_abcdefghijklmnop"

	cases := []struct {
		name    string
		kind    string
		payload string
		field   func(ev lipapi.Event) string
	}{
		{
			name:    "warning_raw_key",
			kind:    protocol.KindWarning,
			payload: `{"message":"upstream failed key=` + apiKey + `"}`,
			field:   func(ev lipapi.Event) string { return ev.WarningMessage },
		},
		{
			name:    "warning_sk_pattern",
			kind:    protocol.KindWarning,
			payload: `{"message":"auth rejected token ` + sk + `"}`,
			field:   func(ev lipapi.Event) string { return ev.WarningMessage },
		},
		{
			name:    "warning_crsr_pattern",
			kind:    protocol.KindWarning,
			payload: `{"message":"cursor credential ` + crsr + ` leaked"}`,
			field:   func(ev lipapi.Event) string { return ev.WarningMessage },
		},
		{
			name:    "error_raw_key",
			kind:    protocol.KindError,
			payload: `{"code":"cursor_sdk_run_failed","message":"boom apiKey=` + apiKey + `"}`,
			field:   func(ev lipapi.Event) string { return ev.ErrorMessage },
		},
		{
			name:    "error_sk_and_crsr",
			kind:    protocol.KindError,
			payload: `{"code":"cursor_sdk_run_failed","message":"bad ` + sk + ` and ` + crsr + `"}`,
			field:   func(ev lipapi.Event) string { return ev.ErrorMessage },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runID := "run-sanitize-" + tc.name
			res, _ := mapBridgeEvent(eventFrame(runID, 1, tc.kind, tc.payload), runID, 1, apiKey)
			require.NoError(t, res.err)
			require.Len(t, res.events, 1)
			msg := tc.field(res.events[0])
			assert.NotContains(t, msg, apiKey)
			assert.NotContains(t, msg, sk)
			assert.NotContains(t, msg, "sk-")
			assert.NotContains(t, msg, crsr)
			assert.NotContains(t, msg, "crsr_")
			assert.NotContains(t, res.events[0].Delta, apiKey)
			assert.NotContains(t, res.events[0].Delta, sk)
			assert.NotContains(t, res.events[0].Delta, crsr)
		})
	}
}
