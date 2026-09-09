package oauthcred_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/oauthcred"
)

func TestLoginMode_MarkerValue(t *testing.T) {
	t.Parallel()
	if oauthcred.LoginModePreProvisionedRefreshOnly != "pre-provisioned-refresh-only" {
		t.Fatalf("unexpected login mode marker %q", oauthcred.LoginModePreProvisionedRefreshOnly)
	}
}

func TestRequireCredential_MissingFile_Actionable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := oauthcred.NewFileStore(path)
	hint := "run login first, then retry"

	_, err := oauthcred.RequireCredential(store, "test-oauth", hint)
	if err == nil {
		t.Fatalf("expected error for missing credential file, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, fmt.Sprintf("%q", path)) {
		t.Fatalf("error must name the credential path, got: %v", err)
	}
	if !strings.Contains(msg, hint) {
		t.Fatalf("error must carry the login hint, got: %v", err)
	}
	if strings.Contains(msg, "no such file") {
		t.Fatalf("error must be actionable, not a raw file-not-found, got: %v", err)
	}
}

func TestRequireCredential_NilStore(t *testing.T) {
	t.Parallel()
	_, err := oauthcred.RequireCredential(nil, "test-oauth", "run login first")
	if err == nil {
		t.Fatalf("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "run login first") {
		t.Fatalf("error must carry the login hint, got: %v", err)
	}
}

func TestRequireCredential_EmptyRecord_Actionable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := oauthcred.NewFileStore(path)
	if err := store.Save(oauthcred.TokenRecord{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err := oauthcred.RequireCredential(store, "test-oauth", "run login first")
	if err == nil {
		t.Fatalf("expected error for token-less record, got nil")
	}
	if !strings.Contains(err.Error(), "holds no tokens") {
		t.Fatalf("error must report empty credential, got: %v", err)
	}
}

func TestRequireCredential_Quarantined_Actionable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := oauthcred.NewFileStore(path)
	if err := store.Save(oauthcred.TokenRecord{
		AccessToken:      "stale-access",
		RefreshToken:     "dead-refresh",
		Quarantined:      true,
		QuarantineReason: "terminal refresh failure",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err := oauthcred.RequireCredential(store, "test-oauth", "explicit re-login required")
	if err == nil {
		t.Fatalf("expected error for quarantined record, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "quarantined") {
		t.Fatalf("error must report quarantine, got: %v", err)
	}
	if !strings.Contains(msg, "explicit re-login required") {
		t.Fatalf("error must carry the login hint, got: %v", err)
	}
}

func TestRequireCredential_RefreshOnly_OK(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := oauthcred.NewFileStore(path)
	if err := store.Save(oauthcred.TokenRecord{RefreshToken: "pre-provisioned-refresh"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	rec, err := oauthcred.RequireCredential(store, "test-oauth", "run login first")
	if err != nil {
		t.Fatalf("refresh-only record must be accepted, got: %v", err)
	}
	if rec.RefreshToken != "pre-provisioned-refresh" {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestRequireCredential_FreshRecord_OK(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := oauthcred.NewFileStore(path)
	if err := store.Save(oauthcred.TokenRecord{
		AccessToken:  "fresh-access",
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(1 * time.Hour),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := oauthcred.RequireCredential(store, "test-oauth", "run login first"); err != nil {
		t.Fatalf("fresh record must be accepted, got: %v", err)
	}
}

func TestRequireCredential_ErrorsNeverLeakTokens(t *testing.T) {
	t.Parallel()
	const secretRefresh = "super-secret-refresh-token-value"
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := oauthcred.NewFileStore(path)
	if err := store.Save(oauthcred.TokenRecord{
		AccessToken:      "stale-access",
		RefreshToken:     secretRefresh,
		Quarantined:      true,
		QuarantineReason: "terminal refresh failure",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err := oauthcred.RequireCredential(store, "test-oauth", "run login first")
	if err == nil {
		t.Fatalf("expected quarantined error, got nil")
	}
	if strings.Contains(err.Error(), secretRefresh) {
		t.Fatalf("error must not leak token material: %v", err)
	}

	// Corrupt the file with content embedding the secret and confirm the load
	// error path also stays clean.
	if err := os.WriteFile(path, []byte("{not-json "+secretRefresh), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	_, err = oauthcred.RequireCredential(store, "test-oauth", "run login first")
	if err == nil {
		t.Fatalf("expected load error, got nil")
	}
	if strings.Contains(err.Error(), secretRefresh) {
		t.Fatalf("load error must not leak token material: %v", err)
	}
}
