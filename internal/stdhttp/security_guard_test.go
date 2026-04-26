package stdhttp

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func withRunningAsAdmin(t *testing.T, fn func() (bool, error)) {
	t.Helper()
	old := runningAsAdmin
	runningAsAdmin = fn
	t.Cleanup(func() { runningAsAdmin = old })
}

func TestValidateStartupSecurity_rejectsAdminUser(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return true, nil })
	cfg := &config.Config{Server: config.ServerConfig{Address: "127.0.0.1:8080"}}
	err := validateStartupSecurity(cfg)
	if err == nil || !strings.Contains(err.Error(), "administrative") {
		t.Fatalf("want administrative rejection, got %v", err)
	}
}

func TestValidateStartupSecurity_rejectsNoAuthNonLoopback(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	cfg := &config.Config{Server: config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeNoAuth}}
	err := validateStartupSecurity(cfg)
	if err == nil || !strings.Contains(err.Error(), "no_auth") {
		t.Fatalf("want no_auth loopback rejection, got %v", err)
	}
}

func TestValidateStartupSecurity_allowsLoopbackNonAdmin(t *testing.T) {
	withRunningAsAdmin(t, func() (bool, error) { return false, nil })
	cfg := &config.Config{Server: config.ServerConfig{Address: "127.0.0.1:8080"}}
	if err := validateStartupSecurity(cfg); err != nil {
		t.Fatal(err)
	}
}
