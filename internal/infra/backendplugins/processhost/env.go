package processhost

import (
	"os"
	"strings"
)

var forbiddenEnvExact = map[string]struct{}{
	"PLUGIN_CLIENT_CERT":  {},
	"PLUGIN_MAGIC_COOKIE": {},
	"PLUGIN_MIN_PORT":     {},
	"PLUGIN_MAX_PORT":     {},
	"LIP_PLUGIN_SECRET":   {},
	"LIP_BOOTSTRAP_KEY":   {},
}

func osLookupEnv(name string) (string, bool) {
	return os.LookupEnv(name)
}

func isForbiddenEnvKey(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	if _, ok := forbiddenEnvExact[u]; ok {
		return true
	}
	if strings.Contains(u, "SECRET") || strings.Contains(u, "PASSWORD") ||
		strings.Contains(u, "TOKEN") || strings.Contains(u, "BOOTSTRAP") ||
		strings.Contains(u, "MAGIC_COOKIE") || strings.Contains(u, "CLIENT_CERT") {
		return true
	}
	return false
}

// buildLaunchEnv returns a non-nil minimal environment. Empty allowlist yields
// a non-nil empty slice (os/exec will not inherit parent env).
func buildLaunchEnv(allow []string, channelFD int, extras []string) ([]string, error) {
	out := make([]string, 0, len(allow)+1+len(extras))
	for _, name := range allow {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if isForbiddenEnvKey(name) {
			return nil, ReasonEnvBootstrapRejected
		}
		if v, ok := osLookupEnv(name); ok {
			out = append(out, name+"="+v)
		}
	}
	if channelFD > 0 {
		out = append(out, "LIP_PLUGIN_CHANNEL_FD="+itoa(channelFD))
	}
	for _, e := range extras {
		if e == "" {
			continue
		}
		key, _, _ := strings.Cut(e, "=")
		if isForbiddenEnvKey(key) {
			return nil, ReasonEnvBootstrapRejected
		}
		out = append(out, e)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
