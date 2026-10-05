package repair

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

func NormalizeASCIIName(name string) string {
	return toolcall.NormalizeToolName(name)
}
