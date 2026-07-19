package cursorsdk

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveBridgeScripts_UseVerboseGoTest(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".."))
	scripts := []string{
		filepath.Join(repoRoot, "scripts", "test-cursor-sdk-live-bridge.ps1"),
		filepath.Join(repoRoot, "scripts", "test-cursor-sdk-live-bridge.sh"),
	}
	for _, path := range scripts {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, path)
		text := string(raw)
		assert.Contains(t, text, "go test -v ", path+" must pass -v so passing stdout JSON is visible")
		assert.Equal(t, 1, strings.Count(text, "go test -v "), path+" must invoke verbose go test once")
		assert.Contains(t, text, "BLOCKED:", path)
	}
}
