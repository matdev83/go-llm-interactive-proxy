//go:build windows

package standardplugins

import "testing"

func TestAlibabaTokenPlanPersistentWindowsEnvironmentFallback(t *testing.T) {
	t.Setenv("ALIBABA_TOKEN_PLAN_API_KEY", "")
	persistent := persistentEnvValue("ALIBABA_TOKEN_PLAN_API_KEY")
	if persistent == "" {
		t.Skip("persistent ALIBABA_TOKEN_PLAN_API_KEY is not configured")
	}
	keys := ResolveUpstreamAPIKeysFromEnv()
	if len(keys.AlibabaTokenPlan) == 0 || keys.AlibabaTokenPlan[0] != persistent {
		t.Fatal("persistent Token Plan key was not loaded")
	}
}
