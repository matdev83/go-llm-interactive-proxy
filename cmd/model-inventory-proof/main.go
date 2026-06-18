package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

const perBackendTimeout = 60 * time.Second

type proofReport struct {
	GeneratedAt string          `json:"generated_at"`
	Backends    []backendReport `json:"backends"`
	Skipped     []skipReport    `json:"skipped"`
}

type backendCandidate struct {
	ID     string
	Config string
}

type backendReport struct {
	ID         string        `json:"id"`
	Status     string        `json:"status"`
	Source     string        `json:"source,omitempty"`
	LoadedAt   string        `json:"loaded_at,omitempty"`
	ModelCount int           `json:"model_count"`
	Models     []modelReport `json:"models"`
	Error      string        `json:"error,omitempty"`
}

type modelReport struct {
	CanonicalID string `json:"canonical_id"`
	NativeID    string `json:"native_id"`
	DisplayName string `json:"display_name,omitempty"`
}

type skipReport struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type awsEnvironment struct {
	Region                  string
	AccessKeyID             bool
	SecretAccessKey         bool
	Profile                 bool
	WebIdentityTokenFile    bool
	RoleARN                 bool
	ContainerCredentialsURI bool
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	keys := pluginreg.ResolveUpstreamAPIKeysFromEnv()
	candidates, skipped := credentialedBackendCandidates(keys, detectAWSEnvironment())

	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBackendsOn(reg, keys); err != nil {
		writeFatal(err)
	}

	client := httpclient.Standard()
	report := proofReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Backends:    make([]backendReport, 0, len(candidates)),
		Skipped:     skipped,
	}
	for _, candidate := range candidates {
		report.Backends = append(report.Backends, loadBackendInventory(ctx, reg, client, candidate))
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		writeFatal(err)
	}
}

func credentialedBackendCandidates(keys pluginreg.UpstreamAPIKeys, awsEnv awsEnvironment) ([]backendCandidate, []skipReport) {
	candidates := make([]backendCandidate, 0, 7)
	skipped := make([]skipReport, 0, 7)

	addStaticKeyBackend := func(id, envName string, values []string) {
		if len(values) == 0 {
			skipped = append(skipped, skipReport{ID: id, Reason: envName + " is not set"})
			return
		}
		candidates = append(candidates, backendCandidate{ID: id, Config: "{}"})
	}

	addStaticKeyBackend(openairesponses.ID, "OPENAI_API_KEY", keys.OpenAI)
	addStaticKeyBackend(openailegacy.ID, "OPENAI_API_KEY", keys.OpenAI)
	addStaticKeyBackend(anthropic.ID, "ANTHROPIC_API_KEY", keys.Anthropic)
	addStaticKeyBackend(gemini.ID, "GEMINI_API_KEY", keys.Gemini)
	addStaticKeyBackend(openrouter.ID, "OPENROUTER_API_KEY", keys.OpenRouter)
	addStaticKeyBackend(nvidia.ID, "NVIDIA_API_KEY", keys.Nvidia)

	if ok, reason := awsEnv.usableForBedrock(); ok {
		candidates = append(candidates, backendCandidate{
			ID:     bedrock.ID,
			Config: "region: " + awsEnv.Region + "\n",
		})
	} else {
		skipped = append(skipped, skipReport{ID: bedrock.ID, Reason: reason})
	}

	return candidates, skipped
}

func detectAWSEnvironment() awsEnvironment {
	region := strings.TrimSpace(os.Getenv("AWS_REGION"))
	if region == "" {
		region = strings.TrimSpace(os.Getenv("AWS_DEFAULT_REGION"))
	}
	return awsEnvironment{
		Region:                  region,
		AccessKeyID:             strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) != "",
		SecretAccessKey:         strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) != "",
		Profile:                 strings.TrimSpace(os.Getenv("AWS_PROFILE")) != "",
		WebIdentityTokenFile:    strings.TrimSpace(os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")) != "",
		RoleARN:                 strings.TrimSpace(os.Getenv("AWS_ROLE_ARN")) != "",
		ContainerCredentialsURI: strings.TrimSpace(os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")) != "" || strings.TrimSpace(os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")) != "",
	}
}

func (e awsEnvironment) usableForBedrock() (bool, string) {
	if e.Region == "" {
		return false, "AWS_REGION or AWS_DEFAULT_REGION is not set"
	}
	if e.AccessKeyID && e.SecretAccessKey {
		return true, "AWS credentials and region are available"
	}
	if e.Profile {
		return true, "AWS profile and region are available"
	}
	if e.WebIdentityTokenFile && e.RoleARN {
		return true, "AWS web identity and region are available"
	}
	if e.ContainerCredentialsURI {
		return true, "AWS container credentials and region are available"
	}
	return false, "AWS credential signal is incomplete"
}

func loadBackendInventory(ctx context.Context, reg *pluginreg.Registry, client *http.Client, candidate backendCandidate) backendReport {
	node, err := yamlNode(candidate.Config)
	if err != nil {
		return backendError(candidate.ID, fmt.Errorf("decode proof config: %w", err))
	}
	be, err := reg.BuildBackend(candidate.ID, node, client)
	if err != nil {
		return backendError(candidate.ID, fmt.Errorf("build backend: %w", err))
	}
	if be.ModelInventory == nil {
		return backendError(candidate.ID, fmt.Errorf("backend does not expose model inventory"))
	}

	backendCtx, cancel := context.WithTimeout(ctx, perBackendTimeout)
	defer cancel()
	snapshot, err := be.ModelInventory.LoadModels(backendCtx)
	if err != nil {
		return backendError(candidate.ID, err)
	}
	return backendReportFromSnapshot(candidate.ID, snapshot)
}

func backendReportFromSnapshot(id string, snapshot modelinventory.Snapshot) backendReport {
	models := make([]modelReport, 0, len(snapshot.Models))
	for _, model := range snapshot.Models {
		models = append(models, modelReport{
			CanonicalID: model.CanonicalID,
			NativeID:    model.NativeID,
			DisplayName: model.DisplayName,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].CanonicalID == models[j].CanonicalID {
			return models[i].NativeID < models[j].NativeID
		}
		return models[i].CanonicalID < models[j].CanonicalID
	})

	loadedAt := ""
	if !snapshot.LoadedAt.IsZero() {
		loadedAt = snapshot.LoadedAt.UTC().Format(time.RFC3339)
	}
	return backendReport{
		ID:         id,
		Status:     "ok",
		Source:     string(snapshot.Source),
		LoadedAt:   loadedAt,
		ModelCount: len(models),
		Models:     models,
	}
}

func backendError(id string, err error) backendReport {
	return backendReport{
		ID:     id,
		Status: "error",
		Models: []modelReport{},
		Error:  err.Error(),
	}
}

func yamlNode(raw string) (yaml.Node, error) {
	var node yaml.Node
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return yaml.Node{}, err
	}
	return node, nil
}

func writeFatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
