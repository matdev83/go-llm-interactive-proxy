package config

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

const (
	maxHTTPHeaderAliases   = 16
	maxHTTPHeaderNameBytes = 64
)

// HTTPHeadersConfig lists extra inbound header names operators may send in
// addition to the standard LIP/vendor defaults. Empty lists keep the defaults.
// Configured names are appended after defaults (first non-empty wins), so
// X-LIP-Route still wins when both a default and an alias are present.
type HTTPHeadersConfig struct {
	APIKey            []string `yaml:"api_key"`
	Route             []string `yaml:"route"`
	SessionID         []string `yaml:"session_id"`
	ResumeToken       []string `yaml:"resume_token"`
	ALegID            []string `yaml:"a_leg_id"`
	SessionHint       []string `yaml:"session_hint"`
	Trace             []string `yaml:"trace"`
	DiagnosticsSecret []string `yaml:"diagnostics_secret"`
}

// Effective merges operator aliases after [lipsdk.DefaultHTTPHeaders].
func (c HTTPHeadersConfig) Effective() lipsdk.HTTPHeaders {
	d := lipsdk.DefaultHTTPHeaders()
	return lipsdk.HTTPHeaders{
		APIKey:            mergeHeaderNames(d.APIKey, c.APIKey),
		Route:             mergeHeaderNames(d.Route, c.Route),
		SessionID:         mergeHeaderNames(d.SessionID, c.SessionID),
		ResumeToken:       mergeHeaderNames(d.ResumeToken, c.ResumeToken),
		ALegID:            mergeHeaderNames(d.ALegID, c.ALegID),
		SessionHint:       mergeHeaderNames(d.SessionHint, c.SessionHint),
		Trace:             mergeHeaderNames(d.Trace, c.Trace),
		DiagnosticsSecret: mergeHeaderNames(d.DiagnosticsSecret, c.DiagnosticsSecret),
	}
}

func mergeHeaderNames(defaults, extra []string) []string {
	if len(extra) > maxHTTPHeaderAliases {
		extra = extra[:maxHTTPHeaderAliases]
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(list []string) {
		for _, raw := range list {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, name)
		}
	}
	add(defaults)
	add(extra)
	return out
}

func validateHTTPHeaders(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	h := cfg.HTTPHeaders
	for _, field := range []struct {
		name  string
		names []string
	}{
		{"http_headers.api_key", h.APIKey},
		{"http_headers.route", h.Route},
		{"http_headers.session_id", h.SessionID},
		{"http_headers.resume_token", h.ResumeToken},
		{"http_headers.a_leg_id", h.ALegID},
		{"http_headers.session_hint", h.SessionHint},
		{"http_headers.trace", h.Trace},
		{"http_headers.diagnostics_secret", h.DiagnosticsSecret},
	} {
		if err := validateHTTPHeaderNameList(field.name, field.names); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPHeaderNameList(field string, names []string) error {
	if len(names) > maxHTTPHeaderAliases {
		return fmt.Errorf("%s: at most %d names", field, maxHTTPHeaderAliases)
	}
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return fmt.Errorf("%s: empty header name", field)
		}
		if err := validateHTTPHeaderName(field, name); err != nil {
			return err
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s: duplicate header name %q", field, name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateHTTPHeaderName(field, name string) error {
	if len(name) > maxHTTPHeaderNameBytes {
		return fmt.Errorf("%s: %q exceeds %d bytes", field, name, maxHTTPHeaderNameBytes)
	}
	if strings.ContainsAny(name, "\r\n\x00:") {
		return fmt.Errorf("%s: %q contains forbidden characters", field, name)
	}
	for i, r := range name {
		if i == 0 {
			if !unicode.IsLetter(r) {
				return fmt.Errorf("%s: %q must start with a letter", field, name)
			}
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			continue
		}
		return fmt.Errorf("%s: %q is not a valid HTTP header token", field, name)
	}
	return nil
}
