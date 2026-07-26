// Command fake-cursor-sdk-bridge is a deterministic stdio fake for Go tests.
// It reads FAKE_BRIDGE_SCRIPT JSON from the environment when set; otherwise
// it uses DefaultScript. No network or Cursor account access.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "doctor":
			fmt.Printf("@cursor/sdk %s\n", protocol.PinnedSDKVersion)
			os.Exit(0)
		}
	}
	script := fakebridge.DefaultScript()
	if raw := os.Getenv("FAKE_BRIDGE_SCRIPT"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &script); err != nil {
			fmt.Fprintf(os.Stderr, "fake-bridge: invalid FAKE_BRIDGE_SCRIPT: %v\n", err)
			os.Exit(2)
		}
	}
	h := fakebridge.New(script)
	runErr := h.Run(os.Stdin, os.Stdout)
	if text := h.StderrText(); text != "" {
		fmt.Fprintln(os.Stderr, text)
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "fake-bridge: %v\n", runErr)
		os.Exit(1)
	}
	if code, ok := h.ExitCode(); ok {
		os.Exit(code)
	}
}
