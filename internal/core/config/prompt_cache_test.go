package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestPromptCacheKeepwarmConfigIsDefaultOnAndBounded(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("prompt_cache:\n  keepwarm:\n    max_idle_duration: 2h\n    renew_timeout: 3s\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	effective := cfg.EffectiveKeepwarm()
	if !effective.Enabled || effective.MaxIdleDuration != 2*time.Hour || effective.RenewTimeout != 3*time.Second {
		t.Fatalf("effective=%+v", effective)
	}
}

// #8: EffectiveKeepwarm must honor an explicit prompt_cache.keepwarm block
// (including an explicit disable) and fall back to defaults only when the block
// is entirely absent.
func TestEffectiveKeepwarmPresenceHoldsExplicitDisable(t *testing.T) {
	var absent Config
	if err := yaml.Unmarshal([]byte(""), &absent); err != nil {
		t.Fatal(err)
	}
	if eff := absent.EffectiveKeepwarm(); !eff.Enabled {
		t.Fatalf("absent prompt_cache must default to enabled: %+v", eff)
	}

	var disabled Config
	if err := yaml.Unmarshal([]byte("prompt_cache:\n  keepwarm:\n    enabled: false\n"), &disabled); err != nil {
		t.Fatal(err)
	}
	if eff := disabled.EffectiveKeepwarm(); eff.Enabled {
		t.Fatalf("explicit keepwarm disable lost: %+v", eff)
	}

	var zero Config
	if eff := zero.EffectiveKeepwarm(); !eff.Enabled {
		t.Fatalf("zero-value prompt_cache must default to enabled: %+v", eff)
	}
	if zero.PromptCache.KeepwarmPresent {
		t.Fatal("zero-value prompt_cache reported present")
	}
}
