package stdhttp_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
)

// These tests guard against drift between the local stdhttp constants and
// the canonical values defined inside the concrete frontend plugin packages.
// Test-only imports of plugin packages are acceptable for boundary alignment.

func TestGeminiFrontendID_matchesCanonical(t *testing.T) {
	t.Parallel()
	if stdhttp.GeminiFrontendID != gemini.ID {
		t.Fatalf("stdhttp.GeminiFrontendID = %q, want gemini.ID = %q", stdhttp.GeminiFrontendID, gemini.ID)
	}
}

func TestSessionHeaders_matchCanonical(t *testing.T) {
	t.Parallel()
	if stdhttp.ExportHeaderAuthoritativeSessionID() != sessionwire.HeaderAuthoritativeSessionID {
		t.Fatalf("stdhttp session ID header = %q, want sessionwire.HeaderAuthoritativeSessionID = %q",
			stdhttp.ExportHeaderAuthoritativeSessionID(), sessionwire.HeaderAuthoritativeSessionID)
	}
	if stdhttp.ExportHeaderResumeToken() != sessionwire.HeaderResumeToken {
		t.Fatalf("stdhttp resume token header = %q, want sessionwire.HeaderResumeToken = %q",
			stdhttp.ExportHeaderResumeToken(), sessionwire.HeaderResumeToken)
	}
}
