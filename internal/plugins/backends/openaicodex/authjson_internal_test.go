package openaicodex

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadAuthJSON_rejectsGroupReadableTokenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits not modeled on windows")
	}
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"tok"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadAuthJSON(path)
	if err == nil {
		t.Fatal("expected permission error for group/other-readable token file")
	}
	if !strings.Contains(err.Error(), "group/other") {
		t.Fatalf("expected group/other in error, got: %v", err)
	}
}

func TestLoadAuthJSON_acceptsOwnerOnlyTokenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits not modeled on windows")
	}
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadAuthJSON(path)
	if err != nil {
		t.Fatalf("expected success for owner-only token file, got: %v", err)
	}
	if got.AccessToken != "tok" {
		t.Fatalf("AccessToken = %q want tok", got.AccessToken)
	}
}
