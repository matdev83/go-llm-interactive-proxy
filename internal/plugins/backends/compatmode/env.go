package compatmode

import (
	"os"
	"strconv"
	"strings"
)

const maxNumberedEnvKeys = 32

// ResolveEnvAPIKeys reads numbered env-root keys into an independent slice for
// one runtime instance. Empty root yields no-auth.
func ResolveEnvAPIKeys(apiKeyEnvVarRoot string) []string {
	root := strings.TrimSpace(apiKeyEnvVarRoot)
	if root == "" {
		return nil
	}
	out := make([]string, 0, maxNumberedEnvKeys)
	if s := strings.TrimSpace(os.Getenv(root)); s != "" {
		out = append(out, s)
	}
	for i := 2; i <= maxNumberedEnvKeys; i++ {
		name := root + "_" + strconv.Itoa(i)
		s := strings.TrimSpace(os.Getenv(name))
		if s == "" {
			break
		}
		out = append(out, s)
	}
	return out
}

// FirstAPIKey returns the first resolved credential or empty for no-auth.
func FirstAPIKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
