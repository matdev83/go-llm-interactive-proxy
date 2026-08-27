package qa

import (
	"strings"
	"testing"
)

func mustMutate(t *testing.T, content, old, new string) string {
	t.Helper()
	mutated := strings.Replace(content, old, new, 1)
	if mutated == content {
		t.Fatalf("mutation failed: target content %q not found in baseline text", old)
	}
	return mutated
}

func mustMutateAll(t *testing.T, content, old, new string) string {
	t.Helper()
	mutated := strings.ReplaceAll(content, old, new)
	if mutated == content {
		t.Fatalf("mutation failed: target content %q not found in baseline text", old)
	}
	return mutated
}
