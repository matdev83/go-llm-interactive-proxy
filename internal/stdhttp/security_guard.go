package stdhttp

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

var runningAsAdmin = detectRunningAsAdmin

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func validateStartupSecurity(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("stdhttp: nil config")
	}
	if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		return fmt.Errorf("stdhttp: %w", err)
	}
	noAuth := cfg.EffectiveServerAuthMode() == config.AuthModeNoAuth
	loopback := config.IsExplicitLoopbackListenAddress(cfg.Server.Address)
	if noAuth && !loopback {
		return fmt.Errorf(
			"stdhttp: no_auth mode requires explicit loopback server.address, got %q",
			cfg.Server.Address,
		)
	}
	isAdmin, err := runningAsAdmin()
	if err != nil {
		return fmt.Errorf("stdhttp: determine administrative privilege: %w", err)
	}
	if isAdmin && !isTruthy(os.Getenv("LIP_ALLOW_ADMIN_USER")) {
		return fmt.Errorf("stdhttp: refusing to start as administrative user on %s", runtime.GOOS)
	}
	return nil
}
