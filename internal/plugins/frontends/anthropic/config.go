package anthropic

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendconfig"

	"gopkg.in/yaml.v3"
)

type Config = frontendconfig.Config

func DecodeConfig(n yaml.Node) (Config, error) {
	return frontendconfig.Decode(n, ID)
}
