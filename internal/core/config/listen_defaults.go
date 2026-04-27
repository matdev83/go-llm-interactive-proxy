package config

import "strings"

// applyDefaultServerListenAddress sets server.address to an explicit loopback host when empty.
// Mode-aware broad-bind rules are enforced in [validateAccessAuth], not here.
func applyDefaultServerListenAddress(cfg *Config) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.Server.Address) == "" {
		cfg.Server.Address = "127.0.0.1:8080"
	}
}
