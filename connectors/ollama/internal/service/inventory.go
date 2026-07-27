package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if i.kind == FactoryKindCloud {
		return i.listCloudModels(ctx, limit)
	}
	return i.listLocalModels(ctx, limit)
}

func (i *instance) listLocalModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	models, err := i.client().ListModels(ctx, limit)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	if len(models) == 0 {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("ollama: local discovery returned no models")
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(models))
	for _, m := range models {
		caps := backendplugin.CapabilitySummary{Streaming: true}
		if c, err := i.fetchCaps(ctx, m.ID); err == nil {
			caps = c
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + m.ID, NativeModelID: m.ID, FactoryKind: i.kind,
			Capabilities: caps,
		})
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: i.kind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func (i *instance) listCloudModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	endpoint := i.cfg.ModelsURL
	if endpoint == "" {
		endpoint = DefaultCloudTags
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	if i.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+i.cfg.APIKey)
	}
	resp, err := i.hc.Do(req)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("ollama-cloud: tags: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("ollama-cloud: tags HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("ollama-cloud: tags json: %w", err)
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(payload.Models))
	for _, row := range payload.Models {
		native := strings.TrimSuffix(strings.TrimSpace(row.Name), "-cloud")
		if native == "" {
			continue
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + native, NativeModelID: native, FactoryKind: i.kind,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("ollama-cloud: discovery returned no models")
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: i.kind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func (i *instance) fetchCaps(ctx context.Context, nativeID string) (backendplugin.CapabilitySummary, error) {
	root := i.cfg.NativeRoot
	if root == "" {
		root = nativeRootFromBase(i.cfg.BaseURL)
	}
	body, _ := json.Marshal(map[string]string{"name": nativeID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(root, "/")+"/api/show", bytes.NewReader(body))
	if err != nil {
		return backendplugin.CapabilitySummary{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.hc.Do(req)
	if err != nil {
		return backendplugin.CapabilitySummary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return backendplugin.CapabilitySummary{}, fmt.Errorf("show HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return backendplugin.CapabilitySummary{}, err
	}
	return capsFromOllama(payload.Capabilities), nil
}
