package credpool_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
)

func TestBackendKeySecrets_fromList(t *testing.T) {
	t.Parallel()
	got, err := credpool.BackendKeySecrets("", []string{" k1 ", "", "k2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("got %#v", got)
	}
}

func TestBackendKeySecrets_primaryFallback(t *testing.T) {
	t.Parallel()
	got, err := credpool.BackendKeySecrets("  sk  ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "sk" {
		t.Fatalf("got %#v", got)
	}
}

func TestBackendKeySecrets_primaryThenListDedup(t *testing.T) {
	t.Parallel()
	got, err := credpool.BackendKeySecrets("primary", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "primary" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("got %#v", got)
	}
}

func TestBackendKeySecrets_dedupPrimaryRepeatedInList(t *testing.T) {
	t.Parallel()
	got, err := credpool.BackendKeySecrets("same", []string{"same", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "same" || got[1] != "other" {
		t.Fatalf("got %#v", got)
	}
}

func TestBackendKeySecrets_emptyErrors(t *testing.T) {
	t.Parallel()
	if _, err := credpool.BackendKeySecrets("", nil); err == nil {
		t.Fatal("expected error")
	}
	if _, err := credpool.BackendKeySecrets("", []string{"", "  "}); err == nil {
		t.Fatal("expected error")
	}
}
