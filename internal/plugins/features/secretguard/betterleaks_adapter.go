package secretguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	blconfig "github.com/betterleaks/betterleaks/v2/config"
	blregexp "github.com/betterleaks/betterleaks/v2/regexp"
	blscan "github.com/betterleaks/betterleaks/v2/scan"
)

// betterLeaksScanner is the feature-private generation handle for BetterLeaks.
// The upstream scanner is immutable after construction and safe for concurrent
// scans; keeping it behind this handle prevents BetterLeaks types from becoming
// part of the feature, runtime, or SDK contracts.
type betterLeaksScanner struct {
	scanner *blscan.Scanner
	facts   DetectorFacts
}

// DetectorFacts is the bounded, request-independent BetterLeaks policy
// inventory for one immutable generation. It deliberately contains no
// upstream scanner/config types or finding content.
type DetectorFacts struct {
	Version           string
	ConfigHash        string
	RuleInventoryHash string
	ActiveRuleCount   int
	MinimumConfidence string
	MaxDecodeDepth    int
	Workers           int
	RuleIDs           []string
}

// newBetterLeaksScanner constructs one precompiled scanner for a runtime
// generation. Disabled policy returns no handle so callers cannot accidentally
// create BetterLeaks work for a disabled detector.
func newBetterLeaksScanner(policy BetterLeaksPolicy) (*betterLeaksScanner, error) {
	if !policy.Enabled {
		return nil, nil
	}
	if err := validateBetterLeaksScannerPolicy(policy); err != nil {
		return nil, err
	}

	cfg, err := blconfig.Default()
	if err != nil {
		return nil, fmt.Errorf("secretguard: betterleaks default config: %w", err)
	}
	selected, facts, err := resolveBetterLeaksConfig(cfg, policy)
	if err != nil {
		return nil, err
	}

	scanner, err := blscan.New(selected,
		blscan.WithRegexEngine(blregexp.Stdlib{}),
		blscan.WithWorkers(policy.Workers),
		blscan.WithMinimumConfidence(policy.MinimumConfidence),
		blscan.WithMaxDecodeDepth(policy.MaxDecodeDepth),
		blscan.WithPrecompile(),
		blscan.WithAllowSignatures(),
	)
	if err != nil {
		return nil, fmt.Errorf("secretguard: construct betterleaks scanner: %w", err)
	}
	return &betterLeaksScanner{scanner: scanner, facts: facts}, nil
}

// policyFacts returns a defensive copy so diagnostics cannot mutate the
// immutable generation inventory.
func (s *betterLeaksScanner) policyFacts() DetectorFacts {
	if s == nil {
		return DetectorFacts{}
	}
	facts := s.facts
	facts.RuleIDs = append([]string(nil), facts.RuleIDs...)
	return facts
}

func resolveBetterLeaksConfig(cfg *blconfig.Config, policy BetterLeaksPolicy) (*blconfig.Config, DetectorFacts, error) {
	if cfg == nil {
		return nil, DetectorFacts{}, fmt.Errorf("secretguard: betterleaks configuration is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, DetectorFacts{}, fmt.Errorf("secretguard: validate betterleaks default config: %w", err)
	}

	disable, err := normalizeRuleSelectors("disable_rules", policy.DisableRules)
	if err != nil {
		return nil, DetectorFacts{}, err
	}
	isolate, err := normalizeRuleSelectors("isolate_rules", policy.IsolateRules)
	if err != nil {
		return nil, DetectorFacts{}, err
	}
	if len(disable) > 0 && len(isolate) > 0 {
		return nil, DetectorFacts{}, fmt.Errorf("secretguard: betterleaks.disable_rules and betterleaks.isolate_rules are mutually exclusive")
	}
	if err := validateRuleSelectors(cfg, "disable_rules", disable); err != nil {
		return nil, DetectorFacts{}, err
	}
	if err := validateRuleSelectors(cfg, "isolate_rules", isolate); err != nil {
		return nil, DetectorFacts{}, err
	}

	selected := cfg
	switch {
	case len(disable) > 0:
		removed := make(map[string]struct{}, len(disable))
		for _, id := range disable {
			removed[id] = struct{}{}
		}
		if hasRemovedRequiredComponent(cfg, removed) {
			return nil, DetectorFacts{}, fmt.Errorf("secretguard: betterleaks.disable_rules cannot remove a required component")
		}
		selected = filterBetterLeaksRules(cfg, func(rule blconfig.Rule) bool {
			_, ok := removed[rule.ID]
			return !ok
		})
	case len(isolate) > 0:
		active := make(map[string]struct{}, len(isolate))
		var addRequired func(string) error
		addRequired = func(id string) error {
			if _, ok := active[id]; ok {
				return nil
			}
			rule, ok := cfg.Rule(id)
			if !ok {
				// Selectors were validated above. This path is defensive and
				// keeps the error bounded if upstream config resolution changes.
				return fmt.Errorf("secretguard: betterleaks isolate_rules has an invalid component closure")
			}
			active[id] = struct{}{}
			for _, component := range rule.Components {
				if component.Optional {
					continue
				}
				if err := addRequired(component.RuleID); err != nil {
					return err
				}
			}
			return nil
		}
		for _, id := range isolate {
			if err := addRequired(id); err != nil {
				return nil, DetectorFacts{}, err
			}
		}
		selected = filterBetterLeaksRules(cfg, func(rule blconfig.Rule) bool {
			_, ok := active[rule.ID]
			return ok
		})
	}
	selected = pruneUnselectedOptionalComponents(selected)
	if err := selected.Validate(); err != nil {
		return nil, DetectorFacts{}, fmt.Errorf("secretguard: selected betterleaks configuration is invalid")
	}

	ruleIDs := make([]string, 0, len(selected.Rules))
	for _, rule := range selected.Rules {
		ruleIDs = append(ruleIDs, rule.ID)
	}
	facts := DetectorFacts{
		Version:           betterLeaksPinnedVersion,
		ConfigHash:        selected.Hash(),
		RuleInventoryHash: hashRuleInventory(ruleIDs),
		ActiveRuleCount:   len(ruleIDs),
		MinimumConfidence: policy.MinimumConfidence,
		MaxDecodeDepth:    policy.MaxDecodeDepth,
		Workers:           policy.Workers,
		RuleIDs:           ruleIDs,
	}
	return selected, facts, nil
}

func validateRuleSelectors(cfg *blconfig.Config, field string, selectors []string) error {
	for _, selector := range selectors {
		// Do not include the supplied selector in the error. Selector values
		// are operator input and may contain sensitive data.
		if _, ok := cfg.Rule(selector); ok {
			continue
		}
		return fmt.Errorf("secretguard: betterleaks.%s contains an unknown rule selector", field)
	}
	return nil
}

func filterBetterLeaksRules(cfg *blconfig.Config, keep func(blconfig.Rule) bool) *blconfig.Config {
	selected := *cfg
	selected.Rules = make([]blconfig.Rule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if keep(rule) {
			selected.Rules = append(selected.Rules, rule)
		}
	}
	return &selected
}

func hasRemovedRequiredComponent(cfg *blconfig.Config, removed map[string]struct{}) bool {
	for _, rule := range cfg.Rules {
		if _, ruleRemoved := removed[rule.ID]; ruleRemoved {
			continue
		}
		for _, component := range rule.Components {
			if component.Optional {
				continue
			}
			if _, componentRemoved := removed[component.RuleID]; componentRemoved {
				return true
			}
		}
	}
	return false
}

// pruneUnselectedOptionalComponents keeps selected roots valid when an
// optional component rule was not selected. Required component references are
// preserved; the isolation closure already retains their rules.
func pruneUnselectedOptionalComponents(cfg *blconfig.Config) *blconfig.Config {
	available := make(map[string]struct{}, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		available[rule.ID] = struct{}{}
	}
	selected := *cfg
	selected.Rules = make([]blconfig.Rule, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		selected.Rules[i] = rule
		if len(rule.Components) == 0 {
			continue
		}
		components := make([]blconfig.Component, 0, len(rule.Components))
		for _, component := range rule.Components {
			if component.Optional {
				if _, ok := available[component.RuleID]; !ok {
					continue
				}
			}
			components = append(components, component)
		}
		selected.Rules[i].Components = components
	}
	return &selected
}

func hashRuleInventory(ruleIDs []string) string {
	canonical := append([]string(nil), ruleIDs...)
	sort.Strings(canonical)
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateBetterLeaksScannerPolicy(policy BetterLeaksPolicy) error {
	if policy.Workers < 1 || policy.Workers > MaxBetterLeaksWorkers {
		return fmt.Errorf("secretguard: betterleaks workers must be between 1 and %d", MaxBetterLeaksWorkers)
	}
	if policy.MaxDecodeDepth < 0 || policy.MaxDecodeDepth > MaxBetterLeaksDecodeDepth {
		return fmt.Errorf("secretguard: betterleaks max decode depth must be between 0 and %d", MaxBetterLeaksDecodeDepth)
	}
	switch policy.MinimumConfidence {
	case blscan.ConfidenceLow, blscan.ConfidenceMedium, blscan.ConfidenceHigh:
	default:
		return fmt.Errorf("secretguard: betterleaks minimum confidence must be low, medium, or high")
	}
	return nil
}
