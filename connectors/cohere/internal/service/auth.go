package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

type StaticTokenProvider string

func (s StaticTokenProvider) Token(context.Context) (string, error) {
	return string(s), nil
}

type TokenProviderFactory func(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle) (TokenProvider, error)

func DefaultTokenProviderFactory(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle) (TokenProvider, error) {
	apiKey := strings.TrimSpace(string(secrets.Values["api_key"]))
	if apiKey == "" {
		apiKey = strings.TrimSpace(string(secrets.Values["apikey"]))
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(string(secrets.Values["token"]))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("cohere: missing required api_key in secrets")
	}
	return StaticTokenProvider(apiKey), nil
}
