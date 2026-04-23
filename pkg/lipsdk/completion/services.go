package completion

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

// Services exposes narrow capabilities for completion gates (design §2, §6).
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}
