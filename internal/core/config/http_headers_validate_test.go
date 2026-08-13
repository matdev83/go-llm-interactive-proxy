package config

import (
	"strings"
	"testing"
)

func TestValidateHTTPHeaderNameList(t *testing.T) {
	t.Parallel()
	if err := validateHTTPHeaderNameList("http_headers.route", []string{"X-Custom-Route"}); err != nil {
		t.Fatalf("valid name: %v", err)
	}
	err := validateHTTPHeaderNameList("http_headers.route", []string{"X LIP Route"})
	if err == nil || !strings.Contains(err.Error(), "http_headers.route") {
		t.Fatalf("want invalid name error, got %v", err)
	}
}

func TestValidate_rejectsInvalidHTTPHeaderAlias(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Plugins:     PluginsConfig{Backends: []PluginConfig{{ID: "b1", Enabled: true}}},
		HTTPHeaders: HTTPHeadersConfig{Route: []string{"X LIP"}},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "http_headers.route") {
		t.Fatalf("want http_headers.route validation error, got %v", err)
	}
}
