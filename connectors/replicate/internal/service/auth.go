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
	token := strings.TrimSpace(string(secrets.Values["api_token"]))
	if token == "" {
		token = strings.TrimSpace(string(secrets.Values["token"]))
	}
	if token == "" {
		token = strings.TrimSpace(string(secrets.Values["api_key"]))
	}
	if token == "" {
		token = strings.TrimSpace(string(secrets.Values["apikey"]))
	}
	if token == "" {
		return nil, fmt.Errorf("replicate: missing required api_token in secrets")
	}
	return StaticTokenProvider(token), nil
}
