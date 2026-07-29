package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	oldVersion := version
	version = "1.2.3-test"
	t.Cleanup(func() { version = oldVersion })

	for _, arg := range []string{"--version", "version"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run([]string{arg}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
			}
			if got := strings.TrimSpace(stdout.String()); got != "lipstd 1.2.3-test" {
				t.Fatalf("output = %q", got)
			}
		})
	}
}
