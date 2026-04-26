package storecontract_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/sqlite"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/storecontract"
)

func TestStoreContract_SQLite(t *testing.T) {
	t.Parallel()
	storecontract.RunAll(t, func(tb *testing.T) app.Store {
		dir, err := os.MkdirTemp("", "securesession-storecontract-")
		if err != nil {
			tb.Fatal(err)
		}
		tb.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "store.db")
		s, err := sqlite.Open(path)
		if err != nil {
			tb.Fatal(err)
		}
		tb.Cleanup(func() { _ = s.Close() })
		return s
	})
}
