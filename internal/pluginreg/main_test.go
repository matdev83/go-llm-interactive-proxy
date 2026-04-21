package pluginreg

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	RegisterStandardBundle()
	os.Exit(m.Run())
}
