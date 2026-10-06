package pathvirtualization_test

import (
	"bytes"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
)

// Spec: b-leg-path-virtualization Task 8.1. design.md "Testing Strategy" asks for a
// fuzz parser that proves panic freedom and a bounded answer over arbitrary untrusted
// bytes. The recognizer runs over a WHOLE completed argument payload, so its input is
// arbitrary bytes rather than a path, which makes total behavior and panic freedom the
// two properties worth pinning.
//
// The corpus is deliberately hostile: truncated JSON, nested containers that never
// close, raw invalid UTF-8, the marker repeated many times, and marker bytes at the
// very end of the input.
func FuzzScanReservedAliasIsTotalAndBounded(f *testing.F) {
	for _, seed := range []string{
		"",
		"/.__lip_v1__/w_" + scanTag + "/a.go",
		`{"path":"/.__lip_v1__/w_` + scanTag + `/a.go"}`,
		`{"path":"/.__lip_v1__/w_short`,
		"/.__lip_v1__",
		"/.__lip_v1__/",
		`.__lip_v1__\w_` + scanTag + `\a.go`,
		`{"content":"/.__lip_v1__/w_` + scanTag + `/","x":[[[[[`,
		`/` + `.__lip_v1__/w_short/` + `x`,
		"\xff\xfe.__lip_v1__\xff/w_short/x",
		`{"path":"` + scanTag + `"}`,
		`\\?\C:\.__lip_v1__\w_` + scanTag + `\a.go`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		got := pathvirtualization.ScanReservedAlias(raw)
		switch got {
		case pathvirtualization.ReservedAliasAbsent,
			pathvirtualization.ReservedAliasMalformedTag,
			pathvirtualization.ReservedAliasWellFormed:
		default:
			t.Fatalf("answer %d is outside the closed vocabulary", int(got))
		}
		// Determinism: the same bytes must always produce the same answer, because
		// the answer decides whether a tool call is released.
		if again := pathvirtualization.ScanReservedAlias(raw); again != got {
			t.Fatalf("non-deterministic answer: %v then %v", got, again)
		}
		// The marker is absent from the answer vocabulary by construction, so a
		// payload with no marker bytes at all must always be absent.
		if !bytes.Contains(raw, []byte(".__lip_v1__")) && got != pathvirtualization.ReservedAliasAbsent {
			t.Fatalf("marker-free payload reported %v", got)
		}
		// A payload carrying a complete alias of a derived mapping must always be
		// recognized: this is the property the caller relies on to refuse closed, so
		// a false negative here would release the reserved namespace.
		mapping, reason := pathvirtualization.DeriveMapping(scanPOSIXRoot)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("derive fixture root: %v", reason)
		}
		document := append(bytes.Repeat([]byte("x"), len(raw)), mapping.VirtualRoot...)
		document = append(document, "src/main.go"...)
		if got := pathvirtualization.ScanReservedAlias(document); got != pathvirtualization.ReservedAliasWellFormed {
			t.Fatalf("a document carrying a derived alias reported %v", got)
		}
	})
}
