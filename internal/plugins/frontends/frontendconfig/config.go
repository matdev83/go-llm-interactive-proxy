package frontendconfig

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ExposeLipUsageExtensions bool `yaml:"expose_lip_usage_extensions"`
}

func Decode(n yaml.Node, id string) (Config, error) {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return Config{}, nil
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return Config{}, nil
	}
	if root.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", id)
	}
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", id, err)
	}
	return cfg, nil
}
