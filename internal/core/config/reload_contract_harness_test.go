package config_test

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
)

// strictDecodeContract delegates to the production StrictDecode path (task 2.1).
func strictDecodeContract(raw []byte) (*config.Config, configsource.Category, error) {
	return config.StrictDecode(raw)
}

func computeEffectiveIdentity(cfg *config.Config) (config.EffectiveIdentity, error) {
	return config.ComputeEffectiveIdentity(cfg)
}
