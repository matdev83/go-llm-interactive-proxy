package openaicred_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
)

func TestCredentialsFromConfig_apiKeysOnly(t *testing.T) {
	t.Parallel()
	got, err := openaicred.CredentialsFromConfig("", []string{" k1 ", "", "k2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Secret != "k1" || got[1].Secret != "k2" {
		t.Fatalf("got %#v", got)
	}
}

func TestCredentialsFromConfig_apiKeyWhenNoList(t *testing.T) {
	t.Parallel()
	got, err := openaicred.CredentialsFromConfig("  sk  ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Secret != "sk" {
		t.Fatalf("got %#v", got)
	}
}

func TestCredentialsFromConfig_primaryThenListOrder(t *testing.T) {
	t.Parallel()
	got, err := openaicred.CredentialsFromConfig("primary", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Secret != "primary" || got[1].Secret != "a" || got[2].Secret != "b" {
		t.Fatalf("got %#v", got)
	}
}

func TestCredentialsFromConfig_emptyError(t *testing.T) {
	t.Parallel()
	_, err := openaicred.CredentialsFromConfig("", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = openaicred.CredentialsFromConfig("", []string{"", "  "})
	if err == nil {
		t.Fatal("expected error")
	}
}
