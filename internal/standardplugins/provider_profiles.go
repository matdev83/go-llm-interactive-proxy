package standardplugins

import (
	"fmt"
	"net/url"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/providerprofiles"
)

// ProviderProfileCatalog is the immutable standard-distribution profile input.
// It is deliberately separate from user PluginConfig rows: private custom
// compatible instances remain accepted without entering this catalog.
func ProviderProfileCatalog() (*providerprofiles.Catalog, error) {
	return providerprofiles.EmbeddedCatalog()
}

// ValidateProviderProfiles performs startup/check-config validation only. It
// does not build adapters, resolve secrets, discover models, or start processes.
func ValidateProviderProfiles() error {
	catalog, err := ProviderProfileCatalog()
	if err != nil {
		return err
	}
	if _, err := catalog.CompileAll(); err != nil {
		return fmt.Errorf("standard provider profiles: %w", err)
	}
	return nil
}

func PrepareProviderProfiles(cfg *config.Config) (*config.Config, error) {
	prepared, err := ExpandProviderProfileRows(cfg)
	if err != nil {
		return nil, err
	}
	if err := ValidateProviderProfiles(); err != nil {
		return nil, err
	}
	return prepared, nil
}

// ProviderProfileDiagnostics is the bounded, secret-safe source projection used
// by composition diagnostics. It intentionally reports identity, family, and
// endpoint origin only; it never includes credentials or raw profile data.
type ProviderProfileDiagnostic struct {
	ID        string
	Family    string
	Endpoint  string
	Auth      bool
	Tokenizer string
}

func ProviderProfileDiagnostics() ([]ProviderProfileDiagnostic, error) {
	catalog, err := ProviderProfileCatalog()
	if err != nil {
		return nil, err
	}
	profiles := catalog.Profiles()
	out := make([]ProviderProfileDiagnostic, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, ProviderProfileDiagnostic{ID: profile.ID, Family: string(profile.Family), Endpoint: endpointIdentity(profile.Endpoint.BaseURL), Auth: profile.Auth.Mode != providerprofiles.AuthNone, Tokenizer: profile.Tokenizer.TokenizerID})
	}
	return out, nil
}

func endpointIdentity(raw string) string {
	// Validation has already checked the URL. Keep only scheme/host so diagnostics
	// cannot accidentally turn into a configuration dump.
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
