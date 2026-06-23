package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/modelcatalog/modelsdev"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type InventoryProviderConfig struct {
	BaseURL     string
	NativeRoot  string
	APIKey      string
	APIKeys     []string
	Credentials []string
	HTTPClient  *http.Client
	Discovery   DiscoveryConfig
	Mode        backendMode
}

type inventoryProvider struct {
	cfg      InventoryProviderConfig
	capsMu   sync.RWMutex
	capsByID map[string]lipapi.BackendCaps
}

func NewInventoryProvider(cfg InventoryProviderConfig) *inventoryProvider {
	if cfg.Mode != backendModeCloud {
		cfg.Mode = backendModeLocal
	}
	return &inventoryProvider{
		cfg:      cfg,
		capsByID: make(map[string]lipapi.BackendCaps),
	}
}

func (p *inventoryProvider) CapsForNative(nativeID string) lipapi.BackendCaps {
	if caps, ok := p.LookupCapsForNative(nativeID); ok {
		return caps
	}
	return defaultModelCaps()
}

func (p *inventoryProvider) LookupCapsForNative(nativeID string) (lipapi.BackendCaps, bool) {
	p.capsMu.RLock()
	defer p.capsMu.RUnlock()
	if caps, ok := p.capsByID[nativeID]; ok {
		return caps, true
	}
	return nil, false
}

func (p *inventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	if !DiscoveryEnabled(p.cfg.Discovery) {
		return modelinventory.Snapshot{}, fmt.Errorf("ollama model discovery: disabled")
	}
	ctx, cancel := p.discoveryContext(ctx)
	defer cancel()

	var warnings []string
	var models []modelinventory.Model
	seen := make(map[string]struct{})

	if discoveryLocalForMode(p.cfg.Mode, p.cfg.Discovery) {
		local, err := p.loadLocalModels(ctx)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("local model discovery failed: %v", err))
		} else {
			for _, m := range local {
				if _, ok := seen[m.NativeID]; ok {
					continue
				}
				seen[m.NativeID] = struct{}{}
				models = append(models, m)
			}
		}
	}

	if discoveryCloudForMode(p.cfg.Mode, p.cfg.Discovery) {
		cloud, err := p.loadCloudModels(ctx)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("cloud model discovery failed: %v", err))
		} else {
			for _, m := range cloud {
				if _, ok := seen[m.NativeID]; ok {
					continue
				}
				seen[m.NativeID] = struct{}{}
				models = append(models, m)
			}
		}
	}

	if len(models) == 0 {
		if len(warnings) > 0 {
			return modelinventory.Snapshot{}, fmt.Errorf("ollama model discovery: all enabled sources failed")
		}
		return modelinventory.Snapshot{}, fmt.Errorf("ollama model discovery returned no models")
	}

	if DiscoveryCatalog(p.cfg.Discovery) && strings.TrimSpace(p.cfg.Discovery.CatalogURL) != "" {
		ids, err := p.loadCatalogModelIDs(ctx)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("models.dev catalog lookup failed: %v", err))
		} else {
			for i := range models {
				models[i].CanonicalID = canonicalIDForNative(p.cfg.Mode, models[i].NativeID, ids)
			}
		}
	}

	if DiscoveryCapabilities(p.cfg.Discovery) {
		p.probeCapabilities(ctx, models)
	}

	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Now(),
		Models:   slices.Clone(models),
		Warnings: slices.Clone(warnings),
	}, nil
}

func (p *inventoryProvider) loadLocalModels(ctx context.Context) ([]modelinventory.Model, error) {
	key, err := firstSecret(p.cfg.APIKey, p.cfg.APIKeys, p.cfg.Credentials)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/") + "/models"
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, p.cfg.HTTPClient, endpoint, map[string]string{"Authorization": "Bearer " + key}, &payload); err != nil {
		return nil, err
	}
	models := make([]modelinventory.Model, 0, len(payload.Data))
	for _, row := range payload.Data {
		native := strings.TrimSpace(row.ID)
		if native == "" {
			continue
		}
		models = append(models, modelinventory.Model{
			CanonicalID: keywordCanonicalID(p.cfg.Mode, native),
			NativeID:    native,
			DisplayName: native,
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("local discovery returned no models")
	}
	return models, nil
}

func (p *inventoryProvider) loadCloudModels(ctx context.Context) ([]modelinventory.Model, error) {
	endpoint := strings.TrimSpace(p.cfg.Discovery.CloudURL)
	if endpoint == "" {
		endpoint = defaultCloudModelsURL
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, p.cfg.HTTPClient, endpoint, nil, &payload); err != nil {
		return nil, err
	}
	models := make([]modelinventory.Model, 0, len(payload.Models))
	for _, row := range payload.Models {
		native := cloudInventoryModelName(row.Name)
		if native == "" {
			continue
		}
		models = append(models, modelinventory.Model{
			CanonicalID: keywordCanonicalID(p.cfg.Mode, native),
			NativeID:    native,
			DisplayName: native,
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("cloud discovery returned no models")
	}
	return models, nil
}

func cloudModelName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasSuffix(name, "-cloud") {
		return name
	}
	return name + "-cloud"
}

func cloudInventoryModelName(name string) string {
	return strings.TrimSuffix(strings.TrimSpace(name), "-cloud")
}

func canonicalIDForNative(mode backendMode, native string, ids []string) string {
	native = strings.TrimSpace(native)
	if native == "" {
		return fallbackCanonicalID(mode, native)
	}
	if len(ids) == 0 {
		return keywordCanonicalID(mode, native)
	}
	want := catalogComparableModelID(native)
	var matched string
	for _, id := range ids {
		if catalogComparableModelID(stripProviderPrefix(id)) != want {
			continue
		}
		if matched != "" {
			return keywordCanonicalID(mode, native)
		}
		matched = id
	}
	if matched == "" {
		return keywordCanonicalID(mode, native)
	}
	provider, _, ok := strings.Cut(strings.TrimSpace(matched), "/")
	if ok && strings.EqualFold(strings.TrimSpace(provider), fallbackCanonicalVendor) {
		return keywordCanonicalID(mode, native)
	}
	return matched
}

func keywordCanonicalID(mode backendMode, native string) string {
	model := stringsTrimCloudSuffixForCanonical(mode, native)
	if model == "" {
		return fallbackCanonicalID(mode, native)
	}
	lower := strings.ToLower(model)
	for _, rule := range vendorKeywordFallbacks {
		if strings.Contains(lower, rule.keyword) {
			return rule.vendor + "/" + model
		}
	}
	return fallbackCanonicalID(mode, native)
}

type vendorKeywordFallback struct {
	keyword string
	vendor  string
}

var vendorKeywordFallbacks = []vendorKeywordFallback{
	{keyword: "nemotron", vendor: "nvidia"},
	{keyword: "gpt", vendor: "openai"},
	{keyword: "claude", vendor: "anthropic"},
	{keyword: "gemini", vendor: "google"},
	{keyword: "gemma", vendor: "google"},
	{keyword: "banana", vendor: "google"},
	{keyword: "kimi", vendor: "moonshotai"},
	{keyword: "glm", vendor: "z-ai"},
	{keyword: "fable", vendor: "anthropic"},
	{keyword: "qwen", vendor: "qwen"},
	{keyword: "deepseek", vendor: "deepseek"},
	{keyword: "minimax", vendor: "minimax"},
	{keyword: "mimo", vendor: "xiaomi"},
	{keyword: "devstral", vendor: "mistralai"},
	{keyword: "rnj", vendor: "essentialai"},
	{keyword: "ministral", vendor: "mistralai"},
	{keyword: "mistral", vendor: "mistralai"},
	{keyword: "llama", vendor: "meta"},
}

func stripProviderPrefix(id string) string {
	_, after, ok := strings.Cut(strings.TrimSpace(id), "/")
	if !ok {
		return strings.TrimSpace(id)
	}
	return after
}

func catalogComparableModelID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimSuffix(id, "-cloud")
	id = strings.TrimSuffix(id, ":latest")
	return id
}

func (p *inventoryProvider) probeCapabilities(ctx context.Context, models []modelinventory.Model) {
	root := strings.TrimRight(strings.TrimSpace(p.cfg.NativeRoot), "/")
	if root == "" {
		root = NativeRootFromBaseURL(p.cfg.BaseURL)
	}
	client := p.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	capsByID := make(map[string]lipapi.BackendCaps, len(models))
	for _, model := range models {
		if caps, err := p.fetchCaps(ctx, client, root, model.NativeID); err == nil {
			capsByID[model.NativeID] = caps
		}
	}
	p.capsMu.Lock()
	p.capsByID = capsByID
	p.capsMu.Unlock()
}

func (p *inventoryProvider) ProbeCapsForNative(ctx context.Context, nativeID string) lipapi.BackendCaps {
	if caps, ok := p.LookupCapsForNative(nativeID); ok {
		return caps
	}
	if ctx == nil {
		return defaultModelCaps()
	}
	ctx, cancel := p.discoveryContext(ctx)
	defer cancel()
	root := strings.TrimRight(strings.TrimSpace(p.cfg.NativeRoot), "/")
	if root == "" {
		root = NativeRootFromBaseURL(p.cfg.BaseURL)
	}
	client := p.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	caps, err := p.fetchCaps(ctx, client, root, nativeID)
	if err != nil {
		return defaultModelCaps()
	}
	p.capsMu.Lock()
	p.capsByID[nativeID] = caps
	p.capsMu.Unlock()
	return caps
}

func (p *inventoryProvider) discoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.cfg.Discovery.Timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, p.cfg.Discovery.Timeout)
}

func (p *inventoryProvider) loadCatalogModelIDs(ctx context.Context) ([]string, error) {
	client := p.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimSpace(p.cfg.Discovery.CatalogURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("models.dev catalog request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models.dev catalog http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models.dev catalog HTTP status %d", resp.StatusCode)
	}
	limited := io.LimitedReader{R: resp.Body, N: 64<<20 + 1}
	body, err := io.ReadAll(&limited)
	if err != nil {
		return nil, fmt.Errorf("models.dev catalog read: %w", err)
	}
	if int64(len(body)) > 64<<20 {
		return nil, fmt.Errorf("models.dev catalog response exceeds %d bytes", 64<<20)
	}
	ids, err := modelsdev.ParseModelIDs(body)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (p *inventoryProvider) fetchCaps(ctx context.Context, client *http.Client, root, nativeID string) (lipapi.BackendCaps, error) {
	body, _ := json.Marshal(map[string]string{"name": nativeID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, root+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("ollama show HTTP status %d", resp.StatusCode)
	}
	var payload struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return CapsFromOllamaCapabilities(payload.Capabilities), nil
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
	return "", fmt.Errorf("ollama model discovery: no API credentials")
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
		return fmt.Errorf("ollama model discovery request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama model discovery HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ollama model discovery HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("ollama model discovery decode: %w", err)
	}
	return nil
}
