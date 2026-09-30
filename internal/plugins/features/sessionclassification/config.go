// Package sessionclassification owns the session-classification feature policy.
package sessionclassification

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const ID = "session-classification"

type Mode string

const (
	ModeHeuristic Mode = "heuristic"
	ModeJev       Mode = "jev"
	ModeHybrid    Mode = "hybrid"
)

const (
	// MaxIgnoredUserAgentPrefixes bounds the configured literal exclusion list.
	MaxIgnoredUserAgentPrefixes = 16
	// MaxIgnoredUserAgentPrefixBytes caps each normalized exclusion prefix.
	MaxIgnoredUserAgentPrefixBytes = 128
	// MaxWorkspaceMarkers bounds marker inspection for one evaluation.
	MaxWorkspaceMarkers = 32
	// MaxWorkspaceMarkerBytes caps each workspace marker inspected by policy.
	MaxWorkspaceMarkerBytes = 128
	// MaxRemoteAttemptsPerSession bounds configured remote attempts.
	MaxRemoteAttemptsPerSession = 5
	// MinRemoteTimeout is the smallest allowed remote timeout.
	MinRemoteTimeout = time.Millisecond
	// MaxRemoteTimeout caps a single remote decision timeout.
	MaxRemoteTimeout = 30 * time.Second
	// MaxRemoteLeaseTTL bounds the shared remote decision lease.
	MaxRemoteLeaseTTL = 2 * time.Minute
	// MaxRemoteRetryBackoff bounds the delay between remote attempts.
	MaxRemoteRetryBackoff = 30 * time.Second
	// RemoteLeaseSafetyMargin ensures lease expiry follows the hard timeout.
	RemoteLeaseSafetyMargin = 100 * time.Millisecond
)

type Config struct {
	Mode      Mode
	Heuristic HeuristicConfig
	Remote    *RemoteConfig
}

type HeuristicConfig struct {
	IgnoredUserAgentPrefixes []string
}

type RemoteConfig struct {
	Provider              string
	APIKeyEnv             string
	Timeout               time.Duration
	MaxAttemptsPerSession int
	LeaseTTL              time.Duration
	RetryBackoff          time.Duration
	PositiveThreshold     float64
}

// DecodeConfig strictly decodes the feature-owned config subtree. Registration
// enablement belongs to the outer plugins.features entry and is not duplicated
// here.
func DecodeConfig(input yaml.Node) (Config, error) {
	cfg := Config{Mode: ModeHeuristic}
	root, empty, err := configRoot(&input)
	if err != nil {
		return Config{}, err
	}
	if empty {
		return cfg, nil
	}
	fields, err := mappingFields(root, "config", "mode", "heuristic", "remote")
	if err != nil {
		return Config{}, err
	}
	if node, ok := fields["mode"]; ok {
		value, valid := scalarString(node, 32)
		if !valid {
			return Config{}, configError("mode must be a bounded string")
		}
		cfg.Mode = Mode(strings.TrimSpace(value))
		if cfg.Mode == "" {
			cfg.Mode = ModeHeuristic
		}
	}

	if node, ok := fields["heuristic"]; ok {
		cfg.Heuristic, err = decodeHeuristicConfig(node)
		if err != nil {
			return Config{}, err
		}
	}
	remoteNode, remotePresent := fields["remote"]
	if remotePresent {
		if cfg.Mode == ModeHeuristic {
			return Config{}, configError("remote settings require jev or hybrid mode")
		}
		remote, decodeErr := decodeRemoteConfig(remoteNode)
		if decodeErr != nil {
			return Config{}, decodeErr
		}
		cfg.Remote = &remote
	}

	switch cfg.Mode {
	case ModeHeuristic:
	case ModeJev, ModeHybrid:
		if !remotePresent {
			return Config{}, configError("jev and hybrid modes require explicit remote settings")
		}
	default:
		return Config{}, configError("mode must be heuristic, jev, or hybrid")
	}
	return cfg, nil
}

func configRoot(node *yaml.Node) (*yaml.Node, bool, error) {
	if node == nil || node.Kind == 0 {
		return nil, true, nil
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, true, nil
		}
		node = node.Content[0]
	}
	if node.Kind == 0 || isNullNode(node) {
		return nil, true, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, false, configError("config must be a mapping or null")
	}
	return node, false, nil
}

func decodeHeuristicConfig(node *yaml.Node) (HeuristicConfig, error) {
	if isNullNode(node) {
		return HeuristicConfig{}, nil
	}
	fields, err := mappingFields(node, "heuristic", "ignored_user_agent_prefixes")
	if err != nil {
		return HeuristicConfig{}, err
	}
	list, ok := fields["ignored_user_agent_prefixes"]
	if !ok {
		return HeuristicConfig{}, nil
	}
	if list == nil || list.Kind != yaml.SequenceNode || list.Tag != "!!seq" {
		return HeuristicConfig{}, configError("ignored_user_agent_prefixes must be a bounded sequence")
	}
	if len(list.Content) > MaxIgnoredUserAgentPrefixes {
		return HeuristicConfig{}, configError("ignored_user_agent_prefixes exceeds its entry limit")
	}

	prefixes := make([]string, 0, len(list.Content))
	seen := make(map[string]struct{}, len(list.Content))
	for _, item := range list.Content {
		value, valid := scalarString(item, MaxIgnoredUserAgentPrefixBytes)
		if !valid {
			return HeuristicConfig{}, configError("ignored_user_agent_prefixes contains an invalid bounded string")
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > MaxIgnoredUserAgentPrefixBytes || hasControlOrInvalidUTF8(value) {
			return HeuristicConfig{}, configError("ignored_user_agent_prefixes contains an invalid bounded string")
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		prefixes = append(prefixes, value)
	}
	return HeuristicConfig{IgnoredUserAgentPrefixes: prefixes}, nil
}

func decodeRemoteConfig(node *yaml.Node) (RemoteConfig, error) {
	fields, err := mappingFields(node, "remote", "provider", "api_key_env", "timeout", "max_attempts_per_session", "lease_ttl", "retry_backoff", "positive_threshold")
	if err != nil {
		return RemoteConfig{}, err
	}
	for _, key := range [...]string{"provider", "api_key_env", "timeout", "max_attempts_per_session", "lease_ttl", "retry_backoff", "positive_threshold"} {
		if _, ok := fields[key]; !ok {
			return RemoteConfig{}, configError("remote settings require every operational field explicitly")
		}
	}

	provider, ok := scalarString(fields["provider"], 32)
	if !ok || provider != "jev" {
		return RemoteConfig{}, configError("remote provider must be jev")
	}
	apiKeyEnv, ok := scalarString(fields["api_key_env"], 128)
	if !ok || !validEnvironmentName(apiKeyEnv) {
		return RemoteConfig{}, configError("api_key_env must name an environment variable")
	}
	timeout, ok := durationString(fields["timeout"])
	if !ok || timeout < MinRemoteTimeout || timeout > MaxRemoteTimeout {
		return RemoteConfig{}, configError("remote timeout is outside its finite bounds")
	}
	attempts, ok := integerScalar(fields["max_attempts_per_session"])
	if !ok || attempts < 1 || attempts > MaxRemoteAttemptsPerSession {
		return RemoteConfig{}, configError("max_attempts_per_session is outside its finite bounds")
	}
	leaseTTL, ok := durationString(fields["lease_ttl"])
	if !ok || leaseTTL > MaxRemoteLeaseTTL || leaseTTL <= timeout+RemoteLeaseSafetyMargin {
		return RemoteConfig{}, configError("lease_ttl must exceed timeout plus the safety margin and remain finite")
	}
	retryBackoff, ok := durationString(fields["retry_backoff"])
	if !ok || retryBackoff < 0 || retryBackoff > MaxRemoteRetryBackoff {
		return RemoteConfig{}, configError("retry_backoff is outside its finite bounds")
	}
	threshold, ok := numberScalar(fields["positive_threshold"])
	if !ok || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold <= 0 || threshold > 1 {
		return RemoteConfig{}, configError("positive_threshold must be greater than zero and at most one")
	}
	return RemoteConfig{
		Provider:              provider,
		APIKeyEnv:             apiKeyEnv,
		Timeout:               timeout,
		MaxAttemptsPerSession: attempts,
		LeaseTTL:              leaseTTL,
		RetryBackoff:          retryBackoff,
		PositiveThreshold:     threshold,
	}, nil
}

func mappingFields(node *yaml.Node, section string, allowed ...string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || node.Tag != "!!map" || len(node.Content)%2 != 0 {
		return nil, configError(section + " must be a mapping")
	}
	if len(node.Content)/2 > len(allowed) {
		return nil, configError(section + " contains unknown or duplicate keys")
	}
	fields := make(map[string]*yaml.Node, len(allowed))
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" || len(keyNode.Value) > 64 {
			return nil, configError(section + " contains an invalid key")
		}
		key := keyNode.Value
		if !isAllowedKey(key, allowed) {
			return nil, configError(section + " contains an unknown key")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, configError(section + " contains a duplicate key")
		}
		fields[key] = node.Content[i+1]
	}
	return fields, nil
}

func isAllowedKey(key string, allowed []string) bool {
	for _, candidate := range allowed {
		if key == candidate {
			return true
		}
	}
	return false
}

func scalarString(node *yaml.Node, maxBytes int) (string, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || len(node.Value) > maxBytes || !utf8.ValidString(node.Value) {
		return "", false
	}
	return node.Value, true
}

func durationString(node *yaml.Node) (time.Duration, bool) {
	value, ok := scalarString(node, 32)
	if !ok {
		return 0, false
	}
	duration, err := time.ParseDuration(value)
	return duration, err == nil
}

func integerScalar(node *yaml.Node) (int, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" || len(node.Value) > 10 {
		return 0, false
	}
	value, err := strconv.ParseInt(node.Value, 10, 32)
	if err != nil {
		return 0, false
	}
	return int(value), true
}

func numberScalar(node *yaml.Node) (float64, bool) {
	if node == nil || node.Kind != yaml.ScalarNode || (node.Tag != "!!int" && node.Tag != "!!float") || len(node.Value) > 32 {
		return 0, false
	}
	value, err := strconv.ParseFloat(node.Value, 64)
	return value, err == nil
}

func isNullNode(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!null"
}

func hasControlOrInvalidUTF8(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validEnvironmentName(value string) bool {
	if len(value) == 0 || len(value) > 128 || !isEnvironmentNameStart(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if !isEnvironmentNameStart(c) && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func isEnvironmentNameStart(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}

func configError(reason string) error {
	return fmt.Errorf("%s: %s", ID, reason)
}
