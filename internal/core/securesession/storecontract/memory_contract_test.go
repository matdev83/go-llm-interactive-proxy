package storecontract_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/storecontract"
)

func TestStoreContract_Memory(t *testing.T) {
	t.Parallel()
	storecontract.RunAll(t, func() app.Store {
		return memory.New(memory.Options{})
	})
}
