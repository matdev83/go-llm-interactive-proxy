package config

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"gopkg.in/yaml.v3"
)

func (p *PromptCacheConfig) UnmarshalYAML(value *yaml.Node) error {
	p.KeepwarmPresent = true
	var raw struct {
		Keepwarm yaml.Node `yaml:"keepwarm"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	if raw.Keepwarm.Kind == 0 {
		p.Keepwarm = keepwarm.DefaultConfig()
		return nil
	}
	wrapped := struct {
		PromptCache struct {
			Keepwarm yaml.Node `yaml:"keepwarm"`
		} `yaml:"prompt_cache"`
	}{}
	wrapped.PromptCache.Keepwarm = raw.Keepwarm
	data, err := yaml.Marshal(wrapped)
	if err != nil {
		return fmt.Errorf("prompt_cache: %w", err)
	}
	cfg, err := keepwarm.ConfigFromYAML(data)
	if err != nil {
		return err
	}
	p.Keepwarm = cfg
	return nil
}
