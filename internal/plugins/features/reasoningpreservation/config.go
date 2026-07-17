package reasoningpreservation

import (
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ActionObserve = "observe"
	ActionRestore = "restore"

	PolicyLogSkip = "log_skip"
	PolicyReject  = "reject"
)

type RuleConfig struct {
	ID            string   `yaml:"id"`
	Backend       string   `yaml:"backend"`
	ModelKeywords []string `yaml:"model_keywords"`
	Enabled       *bool    `yaml:"enabled"`
}

type StateConfig struct {
	TTL                      time.Duration `yaml:"ttl"`
	MaxTurnsPerSession       int           `yaml:"max_turns_per_session"`
	MaxReasoningBytesPerTurn int           `yaml:"max_reasoning_bytes_per_turn"`
	MaxSessionBytes          int           `yaml:"max_session_bytes"`
}

type Config struct {
	Action            string       `yaml:"action"`
	UseBuiltinCatalog bool         `yaml:"use_builtin_catalog"`
	Rules             []RuleConfig `yaml:"rules"`
	OnAmbiguous       string       `yaml:"on_ambiguous"`
	OnUnrepresentable string       `yaml:"on_unrepresentable"`
	OnStateError      string       `yaml:"on_state_error"`
	State             StateConfig  `yaml:"state"`
}

func DecodeConfig(n yaml.Node) (Config, error) {
	_ = n
	return Config{}, ErrNotImplemented
}
