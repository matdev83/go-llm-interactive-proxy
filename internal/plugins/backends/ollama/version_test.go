package ollama_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
)

func TestNativeRootFromBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"http://localhost:11434/v1", "http://localhost:11434"},
		{"http://localhost:11434/v1/", "http://localhost:11434"},
		{"http://localhost:11434/V1", "http://localhost:11434"},
		{"http://localhost:11434/V1/", "http://localhost:11434"},
		{"http://127.0.0.1:11434/v1", "http://127.0.0.1:11434"},
		{"http://localhost:11434", "http://localhost:11434"},
	}
	for _, tt := range tests {
		if got := ollama.NativeRootFromBaseURL(tt.in); got != tt.want {
			t.Fatalf("NativeRootFromBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	t.Parallel()
	v, err := ollama.ParseSemver("0.13.3")
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 0 || v.Minor != 13 || v.Patch != 3 {
		t.Fatalf("parsed = %+v", v)
	}
	v, err = ollama.ParseSemver("1.2.3-rc1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 1 || v.Minor != 2 || v.Patch != 3 {
		t.Fatalf("parsed = %+v", v)
	}
}

func TestSemverAtLeast(t *testing.T) {
	t.Parallel()
	min := ollama.MustParseSemver("0.13.3")
	cases := []struct {
		version string
		want    bool
	}{
		{"0.13.3", true},
		{"0.13.4", true},
		{"0.14.0", true},
		{"0.13.2", false},
		{"0.12.9", false},
	}
	for _, tc := range cases {
		v := ollama.MustParseSemver(tc.version)
		if got := v.AtLeast(min); got != tc.want {
			t.Fatalf("%q AtLeast(0.13.3) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestVersionSupportsResponses(t *testing.T) {
	t.Parallel()
	if !ollama.VersionSupportsResponses("0.13.3") {
		t.Fatal("0.13.3 should support responses")
	}
	if ollama.VersionSupportsResponses("0.13.2") {
		t.Fatal("0.13.2 should not support responses")
	}
}
