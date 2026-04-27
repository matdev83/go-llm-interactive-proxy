//go:build integration

package conformance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

// Tasks 14.1–14.3 (stage checklist): guardrails tying the checklist to concrete test artifacts.
// Cross-API enumeration is enforced by TestMatrixIsCompleteCartesianProduct (matrix_test.go).

func TestStageChecklist14_frontendIntegrationTestsPresent(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	for _, fe := range BundledFrontendIDs() {
		rel := filepath.Join("internal", "plugins", "frontends", frontendDir(fe), "integration_test.go")
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("task 14.1 evidence missing: %s (%v)", rel, err)
		}
	}
}

func TestStageChecklist14_backendIntegrationTestsPresent(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	for _, be := range BundledBackendIDs() {
		rel := filepath.Join("internal", "plugins", "backends", backendDir(be), "integration_test.go")
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("task 14.2 evidence missing: %s (%v)", rel, err)
		}
	}
}

func TestStageChecklist14_referenceClientPackagesPresent(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	for _, dir := range []string{"openairesponses", "openaichat", "anthropicmessages", "gemini"} {
		rel := filepath.Join("internal", "refclient", dir, "client.go")
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("task 14.1 refclient evidence missing: %s (%v)", rel, err)
		}
	}
}

func frontendDir(id string) string {
	switch id {
	case "openai-responses":
		return "openairesponses"
	case "openai-legacy":
		return "openailegacy"
	case "anthropic":
		return "anthropic"
	case "gemini":
		return "gemini"
	default:
		return id
	}
}

func backendDir(id string) string {
	switch id {
	case "openai-responses":
		return "openairesponses"
	case "openai-legacy":
		return "openailegacy"
	case "anthropic":
		return "anthropic"
	case "gemini":
		return "gemini"
	case "bedrock":
		return "bedrock"
	case "acp":
		return "acp"
	default:
		return id
	}
}
