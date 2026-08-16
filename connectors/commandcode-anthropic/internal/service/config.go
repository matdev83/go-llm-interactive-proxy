package service

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	BaseURL    string        `yaml:"base_url"`
	APIKey     string        `yaml:"api_key"`
	Timeout    time.Duration `yaml:"timeout"`
	HTTPClient func() (*http.Client, error)
}

func ParseConfigYAML(b []byte) (Config, error) {
	var raw struct {
		BaseURL string `yaml:"base_url"`
		APIKey  string `yaml:"api_key"`
		Timeout string `yaml:"timeout"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("commandcode-anthropic config: %w", err)
	}
	base := strings.TrimSpace(raw.BaseURL)
	if base == "" {
		base = DefaultURL
	}
	var to time.Duration
	if s := strings.TrimSpace(raw.Timeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return Config{}, fmt.Errorf("commandcode-anthropic timeout %q: %w", s, err)
		}
		to = d
	}
	cfg := Config{
		BaseURL: strings.TrimRight(base, "/"),
		APIKey:  strings.TrimSpace(raw.APIKey),
		Timeout: to,
	}
	cfg.HTTPClient = func() (*http.Client, error) {
		return &http.Client{Timeout: cfg.Timeout}, nil
	}
	return cfg, nil
}
