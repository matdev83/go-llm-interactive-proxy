package sessionclassification

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ID is the canonical standard feature identifier.
const ID = "session-classification"

type Mode string

const (
	ModeHeuristic Mode = "heuristic"
	ModeJev       Mode = "jev"
	ModeHybrid    Mode = "hybrid"

	maxIgnoredUserAgentPrefixes = 32
	maxIgnoredUserAgentBytes    = 256
)

type HeuristicConfig struct {
	IgnoredUserAgentPrefixes []string `yaml:"ignored_user_agent_prefixes"`
}

type RemoteConfig struct {
	Provider              string
	APIKeyEnv             string
	Endpoint              string
	Model                 string
	Timeout               time.Duration
	MaxAttemptsPerSession uint32
	LeaseTTL              time.Duration
	RetryBackoff          time.Duration
	PositiveThreshold     float64
}

type Config struct {
	Mode      Mode
	Heuristic HeuristicConfig
	Remote    RemoteConfig
}

type rawConfig struct {
	Mode      Mode            `yaml:"mode"`
	Heuristic HeuristicConfig `yaml:"heuristic"`
	Remote    struct {
		Provider              string  `yaml:"provider"`
		APIKeyEnv             string  `yaml:"api_key_env"`
		Endpoint              string  `yaml:"endpoint"`
		Model                 string  `yaml:"model"`
		Timeout               string  `yaml:"timeout"`
		MaxAttemptsPerSession uint32  `yaml:"max_attempts_per_session"`
		LeaseTTL              string  `yaml:"lease_ttl"`
		RetryBackoff          string  `yaml:"retry_backoff"`
		PositiveThreshold     float64 `yaml:"positive_threshold"`
	} `yaml:"remote"`
}

func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return Config{Mode: ModeHeuristic}, nil
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return Config{Mode: ModeHeuristic}, nil
	}
	if root.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
	if err := rejectUnknownKeys(root, map[string]map[string]struct{}{
		"": {"mode": {}, "heuristic": {}, "remote": {}},
	}); err != nil {
		return Config{}, err
	}
	var raw rawConfig
	if err := root.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ID, err)
	}
	cfg := Config{Mode: raw.Mode, Heuristic: raw.Heuristic}
	if cfg.Mode == "" {
		cfg.Mode = ModeHeuristic
	}
	cfg.Remote.Provider = strings.TrimSpace(raw.Remote.Provider)
	cfg.Remote.APIKeyEnv = strings.TrimSpace(raw.Remote.APIKeyEnv)
	cfg.Remote.Endpoint = strings.TrimSpace(raw.Remote.Endpoint)
	cfg.Remote.Model = strings.TrimSpace(raw.Remote.Model)
	cfg.Remote.MaxAttemptsPerSession = raw.Remote.MaxAttemptsPerSession
	cfg.Remote.PositiveThreshold = raw.Remote.PositiveThreshold
	var err error
	if strings.TrimSpace(raw.Remote.Timeout) != "" {
		cfg.Remote.Timeout, err = time.ParseDuration(strings.TrimSpace(raw.Remote.Timeout))
		if err != nil {
			return Config{}, fmt.Errorf("%s: remote.timeout: %w", ID, err)
		}
	}
	if strings.TrimSpace(raw.Remote.LeaseTTL) != "" {
		cfg.Remote.LeaseTTL, err = time.ParseDuration(strings.TrimSpace(raw.Remote.LeaseTTL))
		if err != nil {
			return Config{}, fmt.Errorf("%s: remote.lease_ttl: %w", ID, err)
		}
	}
	if strings.TrimSpace(raw.Remote.RetryBackoff) != "" {
		cfg.Remote.RetryBackoff, err = time.ParseDuration(strings.TrimSpace(raw.Remote.RetryBackoff))
		if err != nil {
			return Config{}, fmt.Errorf("%s: remote.retry_backoff: %w", ID, err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("%s: nil config", ID)
	}
	switch c.Mode {
	case ModeHeuristic, ModeJev, ModeHybrid:
	default:
		return fmt.Errorf("%s: mode must be heuristic, jev, or hybrid", ID)
	}
	if len(c.Heuristic.IgnoredUserAgentPrefixes) > maxIgnoredUserAgentPrefixes {
		return fmt.Errorf("%s: too many ignored_user_agent_prefixes", ID)
	}
	for i, p := range c.Heuristic.IgnoredUserAgentPrefixes {
		n := strings.ToLower(strings.TrimSpace(p))
		if n == "" || len(n) > maxIgnoredUserAgentBytes || strings.ContainsRune(n, '\x00') {
			return fmt.Errorf("%s: invalid ignored_user_agent_prefixes[%d]", ID, i)
		}
		c.Heuristic.IgnoredUserAgentPrefixes[i] = n
	}
	if c.Mode == ModeHeuristic {
		return nil
	}
	if c.Remote.Provider != "jev" {
		return fmt.Errorf("%s: remote.provider must be jev", ID)
	}
	if c.Remote.APIKeyEnv == "" || strings.ContainsRune(c.Remote.APIKeyEnv, '\x00') {
		return fmt.Errorf("%s: remote.api_key_env is required", ID)
	}
	if c.Remote.Endpoint == "" {
		c.Remote.Endpoint = "https://api.typesafe.ai/v1/systemone"
	}
	if c.Remote.Model == "" {
		c.Remote.Model = "jev-latest"
	}
	if c.Remote.Timeout <= 0 || c.Remote.Timeout > 30*time.Second {
		return fmt.Errorf("%s: remote.timeout must be >0 and <=30s", ID)
	}
	if c.Remote.MaxAttemptsPerSession == 0 || c.Remote.MaxAttemptsPerSession > 8 {
		return fmt.Errorf("%s: remote.max_attempts_per_session must be 1..8", ID)
	}
	if c.Remote.LeaseTTL <= c.Remote.Timeout+100*time.Millisecond || c.Remote.LeaseTTL > 2*time.Minute {
		return fmt.Errorf("%s: remote.lease_ttl must exceed timeout by at least 100ms and be <=2m", ID)
	}
	if c.Remote.RetryBackoff < 0 || c.Remote.RetryBackoff > time.Hour {
		return fmt.Errorf("%s: remote.retry_backoff must be >=0 and <=1h", ID)
	}
	if c.Remote.PositiveThreshold <= 0 || c.Remote.PositiveThreshold > 1 {
		return fmt.Errorf("%s: remote.positive_threshold must be in (0,1]", ID)
	}
	return nil
}

func rejectUnknownKeys(root yaml.Node, _ map[string]map[string]struct{}) error {
	top := map[string]struct{}{"mode": {}, "heuristic": {}, "remote": {}}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i].Value
		if _, ok := top[k]; !ok {
			return fmt.Errorf("%s: unknown config key %q", ID, k)
		}
		switch k {
		case "heuristic":
			if err := validateMapKeys(*root.Content[i+1], map[string]struct{}{"ignored_user_agent_prefixes": {}}); err != nil {
				return err
			}
		case "remote":
			if err := validateMapKeys(*root.Content[i+1], map[string]struct{}{
				"provider": {}, "api_key_env": {}, "endpoint": {}, "model": {}, "timeout": {},
				"max_attempts_per_session": {}, "lease_ttl": {}, "retry_backoff": {}, "positive_threshold": {},
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateMapKeys(n yaml.Node, allowed map[string]struct{}) error {
	if n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.Tag == "!!null") {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: nested config must be a mapping", ID)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if _, ok := allowed[n.Content[i].Value]; !ok {
			return fmt.Errorf("%s: unknown config key %q", ID, n.Content[i].Value)
		}
	}
	return nil
}
