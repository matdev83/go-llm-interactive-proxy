package configreload_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	mgmtreload "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/configreload"
)

func TestReloadAPI_HTTPStatusGoldens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cat  configreload.ResultCategory
		want int
	}{
		{configreload.ResultPublished, 200},
		{configreload.ResultNoop, 200},
		{configreload.ResultBusy, 409},
		{configreload.ResultRestartRequired, 409},
		{configreload.ResultRetentionBlocked, 409},
		{configreload.ResultInvalid, 422},
		{configreload.ResultSourceIntegrity, 422},
		{configreload.ResultCanceled, 503},
		{configreload.ResultPreparationFailed, 503},
		{configreload.ResultInternalFailed, 503},
	}
	for _, tc := range cases {
		if got := mgmtreload.HTTPStatusFor(tc.cat); got != tc.want {
			t.Fatalf("%s => %d want %d", tc.cat, got, tc.want)
		}
	}
}
