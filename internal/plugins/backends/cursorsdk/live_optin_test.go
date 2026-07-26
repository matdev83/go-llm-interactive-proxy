package cursorsdk

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveOptInReady_BlockedWithoutFlag(t *testing.T) {
	t.Parallel()
	ready, reason := LiveOptInReady(func(string) string { return "" })
	assert.False(t, ready)
	assert.Contains(t, reason, "CURSOR_SDK_LIVE")
}

func TestLiveOptInReady_BlockedWithoutKey(t *testing.T) {
	t.Parallel()
	ready, reason := LiveOptInReady(func(k string) string {
		if k == "CURSOR_SDK_LIVE" {
			return "1"
		}
		return ""
	})
	assert.False(t, ready)
	assert.Contains(t, reason, "CURSOR_API_KEY")
}

func TestLiveOptInReady_ReadyWhenFlagAndKey(t *testing.T) {
	t.Parallel()
	ready, reason := LiveOptInReady(func(k string) string {
		switch k {
		case "CURSOR_SDK_LIVE":
			return "1"
		case "CURSOR_API_KEY":
			return "crsr_test_not_for_network"
		default:
			return ""
		}
	})
	assert.True(t, ready)
	assert.Empty(t, reason)
}

func TestLiveOptIn_DefaultSuiteSkipsWhenNotOptedIn(t *testing.T) {
	if ready, _ := LiveOptInReady(os.Getenv); ready {
		t.Skip("CURSOR_SDK_LIVE=1 + CURSOR_API_KEY set; live suites are make test-cursor-sdk-live / test-cursor-sdk-live-bridge")
	}
	ready, reason := LiveOptInReady(os.Getenv)
	require.False(t, ready)
	require.NotEmpty(t, reason)
}
