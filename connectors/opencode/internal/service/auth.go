package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// Env var constants.
const (
	EnvOpenCodeGoAPIKey  = "OPENCODE_GO_API_KEY"
	EnvOpenCodeZenAPIKey = "OPENCODE_ZEN_API_KEY"
	EnvOpenCodeAPIKey    = "OPENCODE_API_KEY"
)

// ResolveAPIKey resolves credentials for the given OpenCode factory kind.
// Precedence order:
// 1. Secrets bundle from ConfigureRequest (Secrets.Values["api_key"])
// 2. Explicit api_key in config YAML (expanding ${VAR} if present)
// 3. Kind-specific environment variable (OPENCODE_GO_API_KEY / OPENCODE_ZEN_API_KEY)
// 4. OpenCode auth.json on disk (~/.local/share/opencode/auth.json, %LOCALAPPDATA%/opencode/auth.json, etc.)
// 5. Generic OPENCODE_API_KEY fallback
func ResolveAPIKey(kind string, cfg Config, secrets backendplugin.SecretBundle) (string, error) {
	// 1. Secret bundle
	if secret := strings.TrimSpace(string(secrets.Values["api_key"])); secret != "" {
		return normalizeAPIKey(secret), nil
	}

	// 2. Explicit api_key in config with optional ${VAR} expansion
	key := strings.TrimSpace(cfg.APIKey)
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		envVar := strings.TrimSuffix(strings.TrimPrefix(key, "${"), "}")
		key = strings.TrimSpace(os.Getenv(envVar))
	}
	if key != "" {
		return normalizeAPIKey(key), nil
	}

	// 3. Kind-specific env var
	switch kind {
	case FactoryKindGo:
		if env := strings.TrimSpace(os.Getenv(EnvOpenCodeGoAPIKey)); env != "" {
			return normalizeAPIKey(env), nil
		}
	case FactoryKindZen:
		if env := strings.TrimSpace(os.Getenv(EnvOpenCodeZenAPIKey)); env != "" {
			return normalizeAPIKey(env), nil
		}
	}

	// 4. auth.json discovery
	if authKey := loadKeyFromAuthJSON(kind, cfg.AuthJSONPath); authKey != "" {
		return normalizeAPIKey(authKey), nil
	}

	// 5. Generic OPENCODE_API_KEY fallback
	if env := strings.TrimSpace(os.Getenv(EnvOpenCodeAPIKey)); env != "" {
		return normalizeAPIKey(env), nil
	}

	return "", fmt.Errorf("opencode: api_key is required")
}

func normalizeAPIKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if len(trimmed) > 7 && strings.EqualFold(trimmed[:7], "bearer ") {
		return strings.TrimSpace(trimmed[7:])
	}
	return trimmed
}

func loadKeyFromAuthJSON(kind, explicitPath string) string {
	paths := candidateAuthJSONPaths(explicitPath)
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if key := parseKeyFromAuthJSONBytes(kind, data); key != "" {
			return key
		}
	}
	return ""
}

func candidateAuthJSONPaths(explicitPath string) []string {
	if p := strings.TrimSpace(explicitPath); p != "" {
		return []string{p}
	}

	home, _ := os.UserHomeDir()
	var candidates []string
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "opencode", "auth.json"))
		candidates = append(candidates, filepath.Join(home, ".config", "opencode", "auth.json"))
	}

	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			candidates = append(candidates, filepath.Join(local, "opencode", "auth.json"))
		}
		if roaming := strings.TrimSpace(os.Getenv("APPDATA")); roaming != "" {
			candidates = append(candidates, filepath.Join(roaming, "opencode", "auth.json"))
		}
	}

	return candidates
}

type authFileEntry struct {
	Type        string `json:"type"`
	Key         string `json:"key"`
	AccessToken string `json:"access_token"`
	Token       string `json:"token"`
}

func parseKeyFromAuthJSONBytes(kind string, data []byte) string {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ""
	}

	var sections []string
	switch kind {
	case FactoryKindGo:
		sections = []string{"opencode-go", "opencode"}
	case FactoryKindZen:
		sections = []string{"opencode-zen", "zen", "opencode"}
	default:
		sections = []string{"opencode"}
	}

	for _, sec := range sections {
		raw, ok := root[sec]
		if !ok || len(raw) == 0 {
			continue
		}
		var entry authFileEntry
		if err := json.Unmarshal(raw, &entry); err == nil {
			if k := strings.TrimSpace(entry.Key); k != "" {
				return k
			}
			if k := strings.TrimSpace(entry.AccessToken); k != "" {
				return k
			}
			if k := strings.TrimSpace(entry.Token); k != "" {
				return k
			}
		}
		var rawStr string
		if err := json.Unmarshal(raw, &rawStr); err == nil {
			if k := strings.TrimSpace(rawStr); k != "" {
				return k
			}
		}
	}

	return ""
}
