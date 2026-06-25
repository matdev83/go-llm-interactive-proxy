package pluginreg

import (
	"context"
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"gopkg.in/yaml.v3"
)

type bedrockBackendYAML struct {
	Region                   string             `yaml:"region"`
	AccessKeyID              string             `yaml:"access_key_id"`
	SecretAccessKey          string             `yaml:"secret_access_key"`
	SessionToken             string             `yaml:"session_token"`
	BaseEndpoint             string             `yaml:"base_endpoint"`
	DisableHTTPS             bool               `yaml:"disable_https"`
	AllowInsecureNonLoopback bool               `yaml:"allow_insecure_non_loopback"`
	Models                   modelInventoryYAML `yaml:"models"`
}

func backendBedrock(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	var y bedrockBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("bedrock backend config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), bedrock.DefaultLoadConfigTimeout)
	defer cancel()
	return applyConfiguredModelInventory(bedrock.NewWithContext(ctx, bedrock.Config{
		Region:                   y.Region,
		AccessKeyID:              y.AccessKeyID,
		SecretAccessKey:          y.SecretAccessKey,
		SessionToken:             y.SessionToken,
		BaseEndpoint:             y.BaseEndpoint,
		DisableHTTPS:             y.DisableHTTPS,
		AllowInsecureNonLoopback: y.AllowInsecureNonLoopback,
		HTTPClient:               resolveUpstreamHTTP(upstream),
	}), y.Models)
}
