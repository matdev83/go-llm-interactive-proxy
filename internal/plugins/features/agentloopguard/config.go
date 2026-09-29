package agentloopguard

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ID is the standard feature factory id and terminal-decision provider identity.
const ID = providerID

const (
	DefaultVerifierRole             = "loop_guard"
	DefaultVerifierTimeoutSeconds   = 4
	DefaultMaxSemanticContinuations = 3
	DefaultNoProgressLimit          = 2
	DefaultMaxProtocolReprompts     = 1

	MaxVerifierRoleLength     = 128
	MaxVerifierTimeoutSeconds = 300
	MaxSemanticContinuations  = 64
	MaxNoProgressLimit        = 64
	MaxProtocolReprompts      = 3
)

// Strategy selects the mutually-exclusive ALG completion policy.
type Strategy string

const (
	StrategySemanticVerifier Strategy = "semantic_verifier"
	StrategyAttemptCompletion Strategy = "attempt_completion"
)

// ExplicitCompletionPolicy controls legacy semantic-verifier treatment of a
// trusted normalized explicit-completion fact before progress policy evaluation.
type ExplicitCompletionPolicy string

const (
	ExplicitCompletionPolicyTrust  ExplicitCompletionPolicy = "trust"
	ExplicitCompletionPolicyVerify ExplicitCompletionPolicy = "verify"
)

// Config is the generation-local ALG contribution. Strategy omission preserves
// the historical semantic-verifier behavior.
type Config struct {
	Enabled                  bool                     `yaml:"enabled"`
	Strategy                 Strategy                 `yaml:"strategy"`
	VerifierRole             string                   `yaml:"verifier_role"`
	VerifierTimeoutSeconds   int                      `yaml:"verifier_timeout_seconds"`
	VerifierTimeout          time.Duration            `yaml:"-"`
	MaxSemanticContinuations int                      `yaml:"max_semantic_continuations"`
	MaxProtocolReprompts     int                      `yaml:"max_protocol_reprompts"`
	NoProgressLimit          int                      `yaml:"no_progress_limit"`
	ExplicitCompletionPolicy ExplicitCompletionPolicy `yaml:"explicit_completion_policy"`
}

// DecodeConfig parses and validates the nested feature YAML block.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return (Config{}).Normalize()
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return (Config{}).Normalize()
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0:
		return (Config{}).Normalize()
	case yaml.ScalarNode:
		if root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null" {
			return (Config{}).Normalize()
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	case yaml.MappingNode:
		if err := validateKnownKeys(root); err != nil {
			return Config{}, err
		}
		var cfg Config
		if err := root.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		strategy, err := normalizeStrategy(cfg.Strategy)
		if err != nil {
			return Config{}, err
		}
		cfg.Strategy = strategy
		if cfg.Enabled {
			if mappingHasKey(root, "no_progress_limit") && cfg.NoProgressLimit <= 0 {
				return Config{}, fmt.Errorf("%s: no_progress_limit must be positive when enabled", ID)
			}
			if !mappingHasKey(root, "no_progress_limit") {
				cfg.NoProgressLimit = DefaultNoProgressLimit
			}
			switch cfg.Strategy {
			case StrategySemanticVerifier:
				if mappingHasKey(root, "max_protocol_reprompts") {
					return Config{}, fmt.Errorf("%s: max_protocol_reprompts is only valid with strategy %q", ID, StrategyAttemptCompletion)
				}
				if mappingHasKey(root, "verifier_role") && strings.TrimSpace(cfg.VerifierRole) == "" {
					return Config{}, fmt.Errorf("%s: verifier_role must be non-empty when enabled", ID)
				}
				for _, field := range []struct {
					name string
					key  string
					val  int
				}{
					{name: "verifier_timeout_seconds", key: "verifier_timeout_seconds", val: cfg.VerifierTimeoutSeconds},
					{name: "max_semantic_continuations", key: "max_semantic_continuations", val: cfg.MaxSemanticContinuations},
				} {
					if mappingHasKey(root, field.key) && field.val <= 0 {
						return Config{}, fmt.Errorf("%s: %s must be positive when enabled", ID, field.name)
					}
				}
				if !mappingHasKey(root, "verifier_role") {
					cfg.VerifierRole = DefaultVerifierRole
				}
				if !mappingHasKey(root, "verifier_timeout_seconds") {
					cfg.VerifierTimeoutSeconds = DefaultVerifierTimeoutSeconds
				}
				if !mappingHasKey(root, "max_semantic_continuations") {
					cfg.MaxSemanticContinuations = DefaultMaxSemanticContinuations
				}
			case StrategyAttemptCompletion:
				for _, key := range []string{"verifier_role", "verifier_timeout_seconds", "max_semantic_continuations", "explicit_completion_policy"} {
					if mappingHasKey(root, key) {
						return Config{}, fmt.Errorf("%s: %s is incompatible with strategy %q", ID, key, StrategyAttemptCompletion)
					}
				}
				if mappingHasKey(root, "max_protocol_reprompts") && cfg.MaxProtocolReprompts <= 0 {
					return Config{}, fmt.Errorf("%s: max_protocol_reprompts must be positive when enabled", ID)
				}
				if !mappingHasKey(root, "max_protocol_reprompts") {
					cfg.MaxProtocolReprompts = DefaultMaxProtocolReprompts
				}
			}
		}
		return cfg.Normalize()
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

// Normalize fills safe defaults and validates strategy-specific bounds.
// Programmatic construction follows the same mutual-exclusion rules as YAML.
func (c Config) Normalize() (Config, error) {
	strategy, err := normalizeStrategy(c.Strategy)
	if err != nil {
		return Config{}, err
	}
	c.Strategy = strategy

	if c.NoProgressLimit < 0 {
		return Config{}, fmt.Errorf("%s: no_progress_limit must not be negative", ID)
	}
	if c.NoProgressLimit == 0 {
		c.NoProgressLimit = DefaultNoProgressLimit
	}
	if c.NoProgressLimit > MaxNoProgressLimit {
		return Config{}, fmt.Errorf("%s: no_progress_limit exceeds maximum %d", ID, MaxNoProgressLimit)
	}

	switch c.Strategy {
	case StrategySemanticVerifier:
		if c.MaxProtocolReprompts != 0 {
			return Config{}, fmt.Errorf("%s: max_protocol_reprompts is incompatible with strategy %q", ID, StrategySemanticVerifier)
		}
		c.VerifierRole = strings.TrimSpace(c.VerifierRole)
		if c.VerifierRole == "" {
			c.VerifierRole = DefaultVerifierRole
		}
		if len(c.VerifierRole) > MaxVerifierRoleLength {
			return Config{}, fmt.Errorf("%s: verifier_role exceeds maximum length %d", ID, MaxVerifierRoleLength)
		}
		if c.VerifierTimeoutSeconds < 0 {
			return Config{}, fmt.Errorf("%s: verifier_timeout_seconds must not be negative", ID)
		}
		if c.VerifierTimeoutSeconds == 0 {
			c.VerifierTimeoutSeconds = DefaultVerifierTimeoutSeconds
		}
		if c.VerifierTimeoutSeconds > MaxVerifierTimeoutSeconds {
			return Config{}, fmt.Errorf("%s: verifier_timeout_seconds exceeds maximum %d", ID, MaxVerifierTimeoutSeconds)
		}
		c.VerifierTimeout = time.Duration(c.VerifierTimeoutSeconds) * time.Second
		if c.MaxSemanticContinuations < 0 {
			return Config{}, fmt.Errorf("%s: max_semantic_continuations must not be negative", ID)
		}
		if c.MaxSemanticContinuations == 0 {
			c.MaxSemanticContinuations = DefaultMaxSemanticContinuations
		}
		if c.MaxSemanticContinuations > MaxSemanticContinuations {
			return Config{}, fmt.Errorf("%s: max_semantic_continuations exceeds maximum %d", ID, MaxSemanticContinuations)
		}
		policy := ExplicitCompletionPolicy(strings.ToLower(strings.TrimSpace(string(c.ExplicitCompletionPolicy))))
		if policy == "" {
			policy = ExplicitCompletionPolicyTrust
		}
		switch policy {
		case ExplicitCompletionPolicyTrust, ExplicitCompletionPolicyVerify:
			c.ExplicitCompletionPolicy = policy
		default:
			return Config{}, fmt.Errorf("%s: explicit_completion_policy: unknown %q (want trust or verify)", ID, c.ExplicitCompletionPolicy)
		}

	case StrategyAttemptCompletion:
		if strings.TrimSpace(c.VerifierRole) != "" || c.VerifierTimeoutSeconds != 0 || c.VerifierTimeout != 0 ||
			c.MaxSemanticContinuations != 0 || strings.TrimSpace(string(c.ExplicitCompletionPolicy)) != "" {
			return Config{}, fmt.Errorf("%s: verifier-specific settings are incompatible with strategy %q", ID, StrategyAttemptCompletion)
		}
		if c.MaxProtocolReprompts < 0 {
			return Config{}, fmt.Errorf("%s: max_protocol_reprompts must not be negative", ID)
		}
		if c.MaxProtocolReprompts == 0 {
			c.MaxProtocolReprompts = DefaultMaxProtocolReprompts
		}
		if c.MaxProtocolReprompts > MaxProtocolReprompts {
			return Config{}, fmt.Errorf("%s: max_protocol_reprompts exceeds maximum %d", ID, MaxProtocolReprompts)
		}
	}
	return c, nil
}

func normalizeStrategy(in Strategy) (Strategy, error) {
	v := Strategy(strings.ToLower(strings.TrimSpace(string(in))))
	if v == "" {
		return StrategySemanticVerifier, nil
	}
	switch v {
	case StrategySemanticVerifier, StrategyAttemptCompletion:
		return v, nil
	default:
		return "", fmt.Errorf("%s: strategy: unknown %q (want %q or %q)", ID, in, StrategyAttemptCompletion, StrategySemanticVerifier)
	}
}

// Validate checks an already materialized feature configuration.
func (c Config) Validate() error {
	_, err := c.Normalize()
	return err
}

func mappingHasKey(root yaml.Node, key string) bool {
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return true
		}
	}
	return false
}

func validateKnownKeys(root yaml.Node) error {
	known := map[string]struct{}{
		"enabled":                    {},
		"strategy":                   {},
		"verifier_role":              {},
		"verifier_timeout_seconds":   {},
		"max_semantic_continuations": {},
		"max_protocol_reprompts":     {},
		"no_progress_limit":          {},
		"explicit_completion_policy": {},
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if _, ok := known[key]; !ok {
			return fmt.Errorf("%s: unknown field %q", ID, key)
		}
	}
	return nil
}
