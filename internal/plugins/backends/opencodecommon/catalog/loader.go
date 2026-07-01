package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type ModelLoaderConfig struct {
	BaseURL     string
	APIKey      string
	APIKeys     []string
	Credentials []string
	HTTPClient  *http.Client
	Kind        BackendKind
}

func FetchRemoteModelEntries(ctx context.Context, cfg ModelLoaderConfig) ([]ModelEntry, error) {
	if ctx == nil {
		return nil, modelinventory.ErrNilContext
	}
	key, err := firstSecret(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/") + "/models"
	body, err := getJSON(ctx, cfg.HTTPClient, endpoint, map[string]string{"Authorization": "Bearer " + key})
	if err != nil {
		return nil, err
	}
	entries, err := ParseModelsResponse(body)
	if err != nil {
		return nil, err
	}
	return enrichRemoteModelMetadata(cfg.Kind, entries), nil
}

func enrichRemoteModelMetadata(kind BackendKind, entries []ModelEntry) []ModelEntry {
	out := slices.Clone(entries)
	for i := range out {
		if strings.TrimSpace(out[i].AISDKPackage) != "" {
			continue
		}
		out[i].AISDKPackage = aiSDKPackageForRemoteModel(kind, out[i].RawID)
	}
	return out
}

func aiSDKPackageForRemoteModel(kind BackendKind, rawID string) string {
	lower := strings.ToLower(strings.TrimSpace(rawID))
	switch kind {
	case BackendGo:
		if strings.HasPrefix(lower, "minimax-") || strings.HasPrefix(lower, "qwen") {
			return "@ai-sdk/anthropic"
		}
		return "@ai-sdk/openai-compatible"
	case BackendZen:
		switch {
		case strings.HasPrefix(lower, "claude-") || strings.HasPrefix(lower, "qwen"):
			return "@ai-sdk/anthropic"
		case strings.HasPrefix(lower, "gemini-"):
			return "@ai-sdk/google"
		case strings.HasPrefix(lower, "gpt-"):
			return "@ai-sdk/openai"
		default:
			return "@ai-sdk/openai-compatible"
		}
	default:
		return ""
	}
}

func LoadModelEntries(ctx context.Context, cfg ModelLoaderConfig, fallback []ModelEntry) ([]ModelEntry, modelinventory.Source, []string, error) {
	if ctx == nil {
		return nil, "", nil, modelinventory.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, "", nil, err
	}

	entries, err := FetchRemoteModelEntries(ctx, cfg)
	if err == nil && len(entries) > 0 {
		return entries, modelinventory.SourceRemote, []string{}, nil
	}
	if len(fallback) == 0 {
		if err != nil {
			return nil, "", nil, fmt.Errorf("opencodecommon: remote model discovery failed: %w", err)
		}
		return nil, "", nil, fmt.Errorf("opencodecommon: remote model discovery returned no models")
	}
	models := slices.Clone(fallback)
	if len(models) == 0 {
		return nil, "", nil, fmt.Errorf("opencodecommon: fallback model list contained no usable models")
	}
	warnings := []string{}
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("remote model discovery failed: %v", err))
	} else {
		warnings = append(warnings, "remote model discovery returned no models")
	}
	return models, modelinventory.SourceStaticInline, warnings, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string) ([]byte, error) {
	if ctx == nil {
		return nil, modelinventory.ErrNilContext
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("opencodecommon: model discovery request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opencodecommon: model discovery HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("opencodecommon: model discovery HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("opencodecommon: model discovery read: %w", err)
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("opencodecommon: model discovery decode: invalid JSON")
	}
	return body, nil
}

func modelInventorySnapshot(kind BackendKind, entries []ModelEntry, source modelinventory.Source, warnings []string, vendors modelcatalog.VendorResolver) modelinventory.Snapshot {
	return modelinventory.Snapshot{
		Source:   source,
		LoadedAt: time.Now(),
		Models:   InventoryModels(kind, entries, vendors),
		Warnings: warnings,
	}
}

func firstSecret(key string, keySets ...[]string) (string, error) {
	if s := strings.TrimSpace(key); s != "" {
		return s, nil
	}
	for _, keys := range keySets {
		for _, k := range keys {
			if s := strings.TrimSpace(k); s != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("opencodecommon: no API credentials")
}
