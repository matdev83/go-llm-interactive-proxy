package pluginreg

import (
	"strings"
	"testing"
)

func TestValidateCustomBackendPrefix_rejectsEmpty(t *testing.T) {
	t.Parallel()
	if err := validateCustomBackendPrefix(""); err == nil {
		t.Fatal("expected error for empty backend_prefix")
	}
	if err := validateCustomBackendPrefix("   "); err == nil {
		t.Fatal("expected error for whitespace-only backend_prefix")
	}
}

func TestValidateCustomBackendPrefix_rejectsInvalidCharacters(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"acme/prod", "acme:prod", "ac/me", "a:cme"} {
		if err := validateCustomBackendPrefix(prefix); err == nil {
			t.Fatalf("expected error for backend_prefix %q", prefix)
		}
	}
}

func TestValidateCustomBackendPrefix_rejectsReservedStandardPrefixes(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"nvidia", "openrouter", "anthropic", "openai-legacy", "openai-responses", "opencode-go", "opencode-zen"} {
		err := validateCustomBackendPrefix(prefix)
		if err == nil {
			t.Fatalf("expected error for reserved backend_prefix %q", prefix)
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("error for %q = %v, want reserved wording", prefix, err)
		}
	}
}

func TestValidateCustomBackendPrefix_acceptsValidPrefix(t *testing.T) {
	t.Parallel()
	if err := validateCustomBackendPrefix("my-provider"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEnabledCustomBackendPrefixes_rejectsDuplicateEnabledPrefixes(t *testing.T) {
	t.Parallel()
	err := validateEnabledCustomBackendPrefixes([]customBackendPrefixEntry{
		{Enabled: true, BackendPrefix: "same", InstanceID: "a"},
		{Enabled: true, BackendPrefix: "same", InstanceID: "b"},
	})
	if err == nil {
		t.Fatal("expected duplicate backend_prefix error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v, want duplicate wording", err)
	}
}

func TestValidateEnabledCustomBackendPrefixes_ignoresDisabledRows(t *testing.T) {
	t.Parallel()
	err := validateEnabledCustomBackendPrefixes([]customBackendPrefixEntry{
		{Enabled: false, BackendPrefix: "same", InstanceID: "disabled"},
		{Enabled: true, BackendPrefix: "same", InstanceID: "enabled"},
	})
	if err != nil {
		t.Fatalf("disabled rows must not participate in duplicate detection: %v", err)
	}
}

func TestValidateEnabledCustomBackendPrefixes_rejectsReservedAmongEnabled(t *testing.T) {
	t.Parallel()
	err := validateEnabledCustomBackendPrefixes([]customBackendPrefixEntry{
		{Enabled: true, BackendPrefix: "nvidia", InstanceID: "nv-copy"},
	})
	if err == nil {
		t.Fatal("expected reserved backend_prefix error")
	}
}
