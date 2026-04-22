package pluginreg

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if err := RegisterStandardBundle(); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(m.Run())
}
