//go:build windows

package standardplugins

import "golang.org/x/sys/windows/registry"

// persistentEnvValue reads a named environment variable from the persistent
// Windows environment: first the current user's registry Environment key, then
// the machine-wide Session Manager Environment key. It returns an empty string
// when the value is unset or cannot be read. Windows processes inherit a fixed
// environment snapshot from their parent, so this lets the proxy observe a
// credential a user configured after the launcher process started.
func persistentEnvValue(name string) string {
	locations := []struct {
		key  registry.Key
		path string
	}{
		{registry.CURRENT_USER, `Environment`},
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
	}
	for _, location := range locations {
		key, err := registry.OpenKey(location.key, location.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, readErr := key.GetStringValue(name)
		_ = key.Close()
		if readErr == nil && value != "" {
			return value
		}
	}
	return ""
}
