package testkit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// GoldenPathFromRepo joins repo root with path segments under testdata/.
func GoldenPathFromRepo(t *testing.T, repoRoot string, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{repoRoot, "testdata"}, parts...)...)
}

// ReadGoldenBytes loads a fixture file relative to repo root.
func ReadGoldenBytes(t *testing.T, repoRoot string, parts ...string) []byte {
	t.Helper()
	path := GoldenPathFromRepo(t, repoRoot, parts...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	return b
}

// AssertJSONEqual compares two JSON blobs with stable key ordering via compact encoding.
func AssertJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()

	var wantObj, gotObj any
	if err := json.Unmarshal(want, &wantObj); err != nil {
		t.Fatalf("want JSON: %v", err)
	}
	if err := json.Unmarshal(got, &gotObj); err != nil {
		t.Fatalf("got JSON: %v", err)
	}

	wantNorm, err := json.Marshal(wantObj)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gotNorm, err := json.Marshal(gotObj)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}

	if !bytes.Equal(wantNorm, gotNorm) {
		t.Fatalf("JSON mismatch\nwant: %s\ngot: %s", wantNorm, gotNorm)
	}
}
