package config_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"gopkg.in/yaml.v3"
)

// strictDecodeContract is the test-owned effective-loader decode seam for task 1.2.
// Task 2.1 replaces this with the shared production LoadEffective path.
func strictDecodeContract(raw []byte) (*config.Config, configsource.Category, error) {
	if cat, err := configsource.ClassifyBytes(raw, 0); err != nil {
		return nil, cat, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	var cfg config.Config
	if err := dec.Decode(&cfg); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "field") && strings.Contains(msg, "not found") {
			return nil, configsource.CategoryUnknownCoreField, fmt.Errorf("config: %s: %w", configsource.CategoryUnknownCoreField, err)
		}
		return nil, configsource.CategoryMalformedYAML, fmt.Errorf("config: %s", configsource.CategoryMalformedYAML)
	}

	var extra yaml.Node
	err := dec.Decode(&extra)
	switch {
	case err == io.EOF:
		// exactly one document
	case err != nil:
		return nil, configsource.CategoryTrailingContent, fmt.Errorf("config: %s", configsource.CategoryTrailingContent)
	default:
		if extra.Kind != 0 || len(strings.TrimSpace(extra.Value)) > 0 || len(extra.Content) > 0 {
			return nil, configsource.CategoryMultipleDocuments, fmt.Errorf("config: %s", configsource.CategoryMultipleDocuments)
		}
		// Second decode succeeded with an empty node — still treat as trailing/multi.
		return nil, configsource.CategoryMultipleDocuments, fmt.Errorf("config: %s", configsource.CategoryMultipleDocuments)
	}

	return &cfg, configsource.CategoryOK, nil
}

// effectiveIdentity is the private raw/effective identity contract used for no-op
// decisions. PrivateDigest must never appear in logs/APIs; PublicFingerprint is
// secret-safe generation metadata (requirements 3.6–3.8).
type effectiveIdentity struct {
	PrivateDigest     [32]byte
	PublicFingerprint string
}

func computeEffectiveIdentity(cfg *config.Config) (effectiveIdentity, error) {
	if cfg == nil {
		return effectiveIdentity{}, fmt.Errorf("nil config")
	}
	privateRaw, err := yaml.Marshal(cfg)
	if err != nil {
		return effectiveIdentity{}, err
	}
	redacted := redactSecretsForFingerprint(cfg)
	publicRaw, err := yaml.Marshal(redacted)
	if err != nil {
		return effectiveIdentity{}, err
	}
	priv := sha256.Sum256(privateRaw)
	pub := sha256.Sum256(publicRaw)
	return effectiveIdentity{
		PrivateDigest:     priv,
		PublicFingerprint: "cfg_" + hex.EncodeToString(pub[:8]),
	}, nil
}

func redactSecretsForFingerprint(cfg *config.Config) *config.Config {
	cp := *cfg
	if len(cfg.Auth.LocalAPIKeys) == 0 {
		return &cp
	}
	keys := make([]config.AuthLocalAPIKeyRecord, len(cfg.Auth.LocalAPIKeys))
	copy(keys, cfg.Auth.LocalAPIKeys)
	for i := range keys {
		if keys[i].Key != "" {
			keys[i].Key = "[redacted]"
		}
	}
	cp.Auth.LocalAPIKeys = keys
	return &cp
}
