package secretguard

import "testing"

// Detector keys are known configuration, not ignored extension data. Rejecting
// malformed enablement is necessary before presence-aware policy resolution.
func TestDetectorConfig_InvalidEnablement(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"auto_discovered_local_keys", "betterleaks"} {
		for _, value := range []string{"not-a-boolean", "[]", "{}"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				t.Parallel()
				_, err := DecodeConfig(mustYAML(t, "action: block\n"+key+":\n  enabled: "+value+"\n"))
				if err == nil {
					t.Fatal("malformed detector enablement must fail configuration decode")
				}
			})
		}
	}
}

// Requirements 1.8 and 1.9: accepting both-off is deliberate, but does not
// weaken the existing required-action contract or implicitly choose an action.
func TestDetectorConfig_BothOffKeepsActionRequired(t *testing.T) {
	t.Parallel()
	const switches = "auto_discovered_local_keys:\n  enabled: false\nbetterleaks:\n  enabled: false\n"
	if _, err := DecodeConfig(mustYAML(t, switches)); err == nil {
		t.Fatal("both-off still requires an explicit action")
	}
	for _, action := range []string{ActionBlock, ActionRedact, ActionLog} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			cfg, err := DecodeConfig(mustYAML(t, "action: "+action+"\n"+switches))
			if err != nil {
				t.Fatal("both-off configuration must be accepted with an explicit valid action")
			}
			if cfg.Action != action {
				t.Error("detector switches changed the selected action")
			}
		})
	}
}
