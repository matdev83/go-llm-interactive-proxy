package product

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectHostEnvFold_WindowsPathAllowlistCaseInsensitive(t *testing.T) {
	t.Parallel()
	host := []string{`Path=C:\Windows\System32`, "TEMP=C:\\Temp"}
	allow := []string{"PATH", "TEMP"}

	selected := SelectHostEnvFold(host, allow, true)
	require.Contains(t, selected, `Path=C:\Windows\System32`,
		"foldCase=true must match Path against allowlisted PATH and preserve original casing")
	assert.Contains(t, selected, "TEMP=C:\\Temp")

	// Case-sensitive path remains strict when fold is off.
	strict := SelectHostEnvFold(host, allow, false)
	assert.NotContains(t, strict, `Path=C:\Windows\System32`)
	assert.Contains(t, strict, "TEMP=C:\\Temp")
}
