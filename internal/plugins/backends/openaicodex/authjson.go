package openaicodex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type authJSONCredentials struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
}

func resolveConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.AccessToken) != "" {
		if strings.TrimSpace(cfg.AccountID) == "" && strings.TrimSpace(cfg.AuthJSONPath) != "" {
			auth, err := loadAuthJSON(cfg.AuthJSONPath)
			if err == nil && auth.AccountID != "" {
				cfg.AccountID = auth.AccountID
			}
		}
		return cfg, nil
	}

	authPath, explicit := resolveAuthJSONPath(cfg)
	if authPath == "" {
		return cfg, nil
	}

	auth, err := loadAuthJSON(authPath)
	if err != nil {
		if !explicit && os.IsNotExist(err) {
			return cfg, nil
		}
		label := "auth_json_path"
		if !explicit {
			label = "default auth.json"
		}
		return cfg, fmt.Errorf("%s: %s: %w", ID, label, err)
	}
	if auth.AccessToken == "" {
		label := "auth_json_path"
		if !explicit {
			label = "default auth.json"
		}
		return cfg, fmt.Errorf("%s: %s: missing access token", ID, label)
	}
	cfg.AccessToken = auth.AccessToken
	if strings.TrimSpace(cfg.RefreshToken) == "" && auth.RefreshToken != "" {
		cfg.RefreshToken = auth.RefreshToken
	}
	if strings.TrimSpace(cfg.AccountID) == "" && auth.AccountID != "" {
		cfg.AccountID = auth.AccountID
	}
	return cfg, nil
}

func resolveAuthJSONPath(cfg Config) (path string, explicit bool) {
	if p := strings.TrimSpace(cfg.AuthJSONPath); p != "" {
		return p, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".codex", "auth.json"), false
}

func loadAuthJSON(path string) (authJSONCredentials, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return authJSONCredentials{}, fmt.Errorf("path is empty")
	}
	if err := checkTokenFilePermissions(path); err != nil {
		return authJSONCredentials{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return authJSONCredentials{}, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		return authJSONCredentials{}, fmt.Errorf("invalid JSON: %w", err)
	}
	out := authJSONCredentials{
		AccessToken:  jsonRawString(root, "access_token", "accessToken"),
		RefreshToken: jsonRawString(root, "refresh_token", "refreshToken"),
		AccountID:    jsonRawString(root, "account_id", "accountID"),
	}
	if tokensRaw, ok := root["tokens"]; ok {
		var tokens map[string]json.RawMessage
		if err := json.Unmarshal(tokensRaw, &tokens); err == nil {
			out.AccessToken = firstNonEmpty(out.AccessToken, jsonRawString(tokens, "access_token", "accessToken"))
			out.RefreshToken = firstNonEmpty(out.RefreshToken, jsonRawString(tokens, "refresh_token", "refreshToken"))
			out.AccountID = firstNonEmpty(out.AccountID, jsonRawString(tokens, "account_id", "accountID"))
		}
	}
	return out, nil
}

func jsonRawString(m map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v := strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// checkTokenFilePermissions rejects token files readable or writable by group
// or other on Unix, mirroring the Codex CLI auth.json guard. On Windows
// (ACL-based permissions, no meaningful Unix mode bits) it is a no-op.
func checkTokenFilePermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil // let the caller's ReadFile produce the canonical not-exist error
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: token file %q is group/other accessible (mode %o); expected 0600", ID, path, info.Mode().Perm())
	}
	return nil
}
