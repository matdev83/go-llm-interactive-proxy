package modeldiscover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type OpenAICompatibleModelsProvider struct {
	BaseURL           string
	APIKey            string
	APIKeys           []string
	Credentials       []string
	HTTPClient        *http.Client
	CanonicalPrefix   string
	PreserveVendorIDs bool
}

func (p OpenAICompatibleModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	key, err := firstSecret(p.APIKey, p.APIKeys, p.Credentials)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/") + "/models"
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, p.HTTPClient, endpoint, map[string]string{"Authorization": "Bearer " + key}, &payload); err != nil {
		return modelinventory.Snapshot{}, err
	}
	models := make([]modelinventory.Model, 0, len(payload.Data))
	for _, row := range payload.Data {
		native := strings.TrimSpace(row.ID)
		if native == "" {
			continue
		}
		canonical := canonicalWithPrefix(p.CanonicalPrefix, native, p.PreserveVendorIDs)
		models = append(models, modelinventory.Model{CanonicalID: canonical, NativeID: native, DisplayName: native})
	}
	return snapshot(models)
}

type AnthropicModelsProvider struct {
	BaseURL         string
	APIKey          string
	APIKeys         []string
	HTTPClient      *http.Client
	CanonicalPrefix string
}

func (p AnthropicModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	key, err := firstSecret(p.APIKey, p.APIKeys)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/") + "/v1/models"
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	headers := map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
	}
	if err := getJSON(ctx, p.HTTPClient, endpoint, headers, &payload); err != nil {
		return modelinventory.Snapshot{}, err
	}
	models := make([]modelinventory.Model, 0, len(payload.Data))
	prefix := strings.Trim(strings.TrimSpace(p.CanonicalPrefix), "/")
	if prefix == "" {
		prefix = "anthropic"
	}
	for _, row := range payload.Data {
		native := strings.TrimSpace(row.ID)
		if native == "" {
			continue
		}
		models = append(models, modelinventory.Model{
			CanonicalID: prefix + "/" + native,
			NativeID:    native,
			DisplayName: strings.TrimSpace(row.DisplayName),
		})
	}
	return snapshot(models)
}

type GeminiModelsProvider struct {
	BaseURL    string
	APIKey     string
	APIKeys    []string
	HTTPClient *http.Client
}

func (p GeminiModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	key, err := firstSecret(p.APIKey, p.APIKeys)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	// Gemini model enumeration requires API-key authentication in the query string.
	// Treat endpoint as secret-bearing: do not log or dump this URL without redacting "key".
	endpoint := base + "/v1beta/models?key=" + url.QueryEscape(key)
	var payload struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := getJSON(ctx, p.HTTPClient, endpoint, nil, &payload); err != nil {
		return modelinventory.Snapshot{}, redactURLQueryError(err, "key")
	}
	models := make([]modelinventory.Model, 0, len(payload.Models))
	for _, row := range payload.Models {
		native := strings.TrimPrefix(strings.TrimSpace(row.Name), "models/")
		if native == "" {
			continue
		}
		models = append(models, modelinventory.Model{
			CanonicalID: "google/" + native,
			NativeID:    native,
			DisplayName: strings.TrimSpace(row.DisplayName),
		})
	}
	return snapshot(models)
}

func firstSecret(key string, keySets ...[]string) (string, error) {
	if s := strings.TrimSpace(key); s != "" {
		return s, nil
	}
	for _, keys := range keySets {
		for _, key := range keys {
			if s := strings.TrimSpace(key); s != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("model discovery: no API credentials")
}

func redactURLQueryError(err error, queryKeys ...string) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}
	cp := *urlErr
	if u, parseErr := url.Parse(cp.URL); parseErr == nil {
		q := u.Query()
		for _, key := range queryKeys {
			if q.Has(key) {
				q.Set(key, "REDACTED")
			}
		}
		u.RawQuery = q.Encode()
		cp.URL = u.String()
	}
	return &cp
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, out any) error {
	if ctx == nil {
		return modelinventory.ErrNilContext
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("model discovery request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("model discovery HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("model discovery HTTP status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("model discovery decode: %w", err)
	}
	return nil
}

func snapshot(models []modelinventory.Model) (modelinventory.Snapshot, error) {
	if len(models) == 0 {
		return modelinventory.Snapshot{}, fmt.Errorf("model discovery returned no models")
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Now(),
		Models:   slices.Clone(models),
		Warnings: []string{},
	}, nil
}

func canonicalWithPrefix(prefix, native string, preserveVendor bool) string {
	native = strings.TrimSpace(native)
	if preserveVendor && strings.Contains(native, "/") {
		return native
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	return prefix + "/" + strings.TrimPrefix(native, "models/")
}
