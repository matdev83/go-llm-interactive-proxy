package pathvirtualization_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
)

// Spec: b-leg-path-virtualization Task 8.1. The lexical core already recognizes
// the fixed V1 reserved alias namespace (Task 2.3) and rejects a malformed or stale
// one from a PARSEABLE path. Task 8.1 adds the one case that has no parseable path at
// all: a completed tool-call argument document the runtime could not read, which may
// still carry a model-emitted alias (design.md "Error Handling": "a recognized
// applicable alias must never bypass required expansion and reach the client").
//
// Deciding that needs a lexical recognizer over raw bytes, so this file pins what it
// recognizes, what it refuses, and that its vocabulary is bounded and content-free.
// It is deliberately NOT a second alias recognizer: it recognizes the marker at a
// segment boundary and validates the tag with the namespace's own alphabet.
//
// Fixture roots are LONG on purpose. A short root is a supported root whose alias is
// correctly not shorter than the root itself, which leaves virtualization inactive
// (requirement 1.4) and would make every expectation here vacuous.
//
// scanTag is a syntactically VALID but fictional workspace tag, spelled from the
// unpadded base32 alphabet the frozen derivation emits (a-z and 2-7). It never has to
// name a real workspace: the scan decides only whether bytes spell the reserved
// namespace, never whether the alias names the current one.

const (
	scanPOSIXRoot         = "/home/dev/workspaces/lip-path-virtualization-worktree"
	scanPOSIXRootOther    = "/home/dev/workspaces/lip-path-virtualization-main-checkout"
	scanDriveRoot         = `C:\Users\dev\source\repos\lip-path-virtualization-worktree`
	scanDriveRootOther    = `D:\Users\dev\source\repos\lip-path-virtualization-worktree`
	scanUNCRoot           = `\\build01\dev\source\repos\lip-path-virtualization-worktree`
	scanExtendedDriveRoot = `\\?\C:\Users\dev\source\repos\lip-path-virtualization-worktree`
	scanExtendedUNCRoot   = `\\?\UNC\build01\dev\source\repos\lip-path-virtualization-worktree`

	// scanTag is twenty base32 characters: the exact width a V1 workspace tag
	// carries.
	scanTag = "abcdefghijklmnopqrst"

	// scanTagUpper is the same twenty characters in the upper-case spelling a Windows
	// flavor accepts, because Windows matching is ASCII case-insensitive.
	scanTagUpper = "ABCDEFGHIJKLMNOPQRST"
)

// TestScanReservedAliasRecognizesTheFixedV1Forms walks every derived alias root
// form and requires each one to be recognized as well-formed, both bare and inside a
// JSON argument document, because the scan runs over the document.
func TestScanReservedAliasRecognizesTheFixedV1Forms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		project string
		suffix  string
	}{
		{name: "posix", project: scanPOSIXRoot, suffix: "src/main.go"},
		{name: "windows_drive", project: scanDriveRoot, suffix: `src\main.go`},
		{name: "windows_drive_other_drive", project: scanDriveRootOther, suffix: `src\main.go`},
		{name: "windows_unc", project: scanUNCRoot, suffix: `src\main.go`},
		{name: "windows_extended_drive", project: scanExtendedDriveRoot, suffix: `src\main.go`},
		{name: "windows_extended_unc", project: scanExtendedUNCRoot, suffix: `src\main.go`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mapping, reason := pathvirtualization.DeriveMapping(tc.project)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("derive %q: reason %v", tc.project, reason)
			}
			if mapping.VirtualRoot == "" {
				t.Fatalf("fixture root %q must derive an active alias (requirement 1.4)", tc.project)
			}
			if len(mapping.WorkspaceTag) != 20 {
				t.Fatalf("the frozen V1 tag must be 20 characters, got %d", len(mapping.WorkspaceTag))
			}
			alias := mapping.VirtualRoot + tc.suffix
			if got := pathvirtualization.ScanReservedAlias([]byte(alias)); got != pathvirtualization.ReservedAliasWellFormed {
				t.Fatalf("scan(%q)=%v want well-formed", alias, got)
			}
			document := fmt.Sprintf(`{"path":%q,"content":"kept"}`, alias)
			if got := pathvirtualization.ScanReservedAlias([]byte(document)); got != pathvirtualization.ReservedAliasWellFormed {
				t.Fatalf("scan(document carrying %q)=%v want well-formed", alias, got)
			}
		})
	}
}

// TestScanReservedAliasRecognizesSegmentBoundariesOnly is the false-positive guard
// for the half of the rule that runs before the tag decision. A substring that merely
// CONTAINS the marker bytes is not the reserved namespace: a real path can name a
// directory `my.__lip_v1__`, and requirement 1.8's namespace is the marker as a
// complete segment.
func TestScanReservedAliasRecognizesSegmentBoundariesOnly(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"/home/dev/my.__lip_v1__/src/main.go",
		"/home/dev/.__lip_v1__x/src/main.go",
		"/home/dev/.__lip_v1__x/w_" + scanTag + "/src/main.go",
		"prefix.__lip_v1__/w_" + scanTag + "/src/main.go",
		`{"content":".__lip_v1__ is the reserved namespace"}`,
		`{"path":"/home/dev/x.__lip_v1__/src/main.go"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if got := pathvirtualization.ScanReservedAlias([]byte(raw)); got != pathvirtualization.ReservedAliasAbsent {
				t.Fatalf("scan(%q)=%v want absent", raw, got)
			}
		})
	}
}

// TestScanReservedAliasDistinguishesMalformedTags pins the two-step answer: a
// marker at a segment boundary followed by an unusable tag segment is a RECOGNIZED
// malformed alias, never an ordinary path. That distinction is what lets a caller
// refuse a malformed reserved alias closed (requirement 4.4) rather than release it as
// an ordinary client path.
func TestScanReservedAliasDistinguishesMalformedTags(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "no_tag_segment", raw: `{"path":"/.__lip_v1__/src/main.go"}`},
		{name: "tag_prefix_only", raw: `{"path":"/.__lip_v1__/w_/src/main.go"}`},
		{name: "tag_too_short", raw: `{"path":"/.__lip_v1__/w_short/src/main.go"}`},
		{name: "nineteen_characters", raw: `{"path":"/.__lip_v1__/w_abcdefghijklmnopqrs/src/main.go"}`},
		{name: "twenty_one_characters", raw: `{"path":"/.__lip_v1__/w_abcdefghijklmnopqrstu/src/main.go"}`},
		{name: "digit_outside_the_base32_alphabet", raw: `{"path":"/.__lip_v1__/w_abcdefghijklmnopqr0t/src/main.go"}`},
		{name: "wrong_tag_prefix", raw: `{"path":"/.__lip_v1__/x_abcdefghijklmnopqrst/src/main.go"}`},
		{name: "prose_mention_with_a_short_tag", raw: `{"content":"see /.__lip_v1__/w_abcdefghijklmno pqrst/ for details"}`},
		{name: "prose_mention_with_a_nineteen_tag", raw: `{"content":"echo /.__lip_v1__/w_abcdefghijklmnopqrs t/"}`},
		{name: "windows_drive_form_with_a_short_tag", raw: `C:\.__lip_v1__\w_abcdefghijklmnopqrs\src\main.go`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := pathvirtualization.ScanReservedAlias([]byte(tc.raw)); got != pathvirtualization.ReservedAliasMalformedTag {
				t.Fatalf("scan(%q)=%v want malformed-tag", tc.raw, got)
			}
		})
	}
}

// TestScanReservedAliasAcceptsEitherTagCase proves the scan validates the TAG SHAPE
// and not a flavor's case rules. Whether an upper-case tag names this workspace is the
// mapping's answer on a parseable path, and a scan that pre-empted it would report a
// shape failure for a well-formed Windows alias spelled in upper case.
func TestScanReservedAliasAcceptsEitherTagCase(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "posix_lower_case", raw: `{"path":"/.__lip_v1__/w_` + scanTag + `/src/main.go"}`},
		{name: "posix_upper_case", raw: `{"path":"/.__lip_v1__/w_` + scanTagUpper + `/src/main.go"}`},
		{name: "windows_drive_upper_case", raw: `C:\.__lip_v1__\w_` + scanTagUpper + `\src\main.go`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := pathvirtualization.ScanReservedAlias([]byte(tc.raw)); got != pathvirtualization.ReservedAliasWellFormed {
				t.Fatalf("scan(%q)=%v want well-formed", tc.raw, got)
			}
		})
	}
}

// TestScanReservedAliasIsIndependentOfMapping proves the scan answers only "do these
// bytes spell the reserved namespace", never "does this alias name MY workspace". The
// stale-workspace decision needs a mapping, and an unparseable document may arrive
// before one is even derivable, so that decision must not be smuggled into a byte scan.
func TestScanReservedAliasIsIndependentOfMapping(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(scanPOSIXRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive: reason %v", reason)
	}
	other, reason := pathvirtualization.DeriveMapping(scanPOSIXRootOther)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive other: reason %v", reason)
	}
	if other.WorkspaceTag == mapping.WorkspaceTag {
		t.Fatal("fixture roots must derive different workspace tags")
	}
	for _, alias := range []string{
		mapping.VirtualRoot + "src/main.go",
		other.VirtualRoot + "src/main.go",
	} {
		if got := pathvirtualization.ScanReservedAlias([]byte(alias)); got != pathvirtualization.ReservedAliasWellFormed {
			t.Fatalf("scan(%q)=%v want well-formed", alias, got)
		}
	}
}

// TestScanReservedAliasReasonLabelsAreClosedAndContentFree pins the bounded
// vocabulary: three members, fixed labels, an unknown-value fallback, and nothing
// derived from the scanned bytes. That is what makes the answer safe as an
// observability dimension and as the reason a tool call is refused.
func TestScanReservedAliasReasonLabelsAreClosedAndContentFree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		presence pathvirtualization.ReservedAliasPresence
		want     string
	}{
		{presence: pathvirtualization.ReservedAliasAbsent, want: "absent"},
		{presence: pathvirtualization.ReservedAliasMalformedTag, want: "malformed_tag"},
		{presence: pathvirtualization.ReservedAliasWellFormed, want: "well_formed"},
		{presence: pathvirtualization.ReservedAliasPresence(200), want: "unknown"},
	} {
		if got := tc.presence.String(); got != tc.want {
			t.Fatalf("ReservedAliasPresence(%d).String()=%q want %q", int(tc.presence), got, tc.want)
		}
	}
	for _, label := range []string{"absent", "malformed_tag", "well_formed", "unknown"} {
		for _, forbidden := range []string{
			".__lip_v1__", "w_", "w_" + scanTag, "/home", `\Users`, "C:", "main.go",
		} {
			if strings.Contains(label, forbidden) {
				t.Fatalf("label %q leaks %q", label, forbidden)
			}
		}
	}
}

// TestScanReservedAliasEmptyAndMarkerFreeInputsAreAbsent pins the total answer for
// the empty and marker-free cases, so a caller can scan without a length or contents
// check of its own.
func TestScanReservedAliasEmptyAndMarkerFreeInputsAreAbsent(t *testing.T) {
	t.Parallel()

	for _, raw := range [][]byte{nil, {}, []byte(""), []byte("{}"), []byte(`{"path":""}`), []byte("null")} {
		if got := pathvirtualization.ScanReservedAlias(raw); got != pathvirtualization.ReservedAliasAbsent {
			t.Fatalf("scan(%q)=%v want absent", raw, got)
		}
	}
}

// TestScanReservedAliasFindsTheMarkerAfterALongPrelude keeps the recognizer honest
// about real documents: the alias sits at an unpredictable offset inside a completed
// argument payload, not at a fixed prefix.
func TestScanReservedAliasFindsTheMarkerAfterALongPrelude(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(scanPOSIXRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("derive: reason %v", reason)
	}
	prelude := `{"content":"` + strings.Repeat("x", 64<<10) + `","path":` +
		fmt.Sprintf("%q", mapping.VirtualRoot+"src/main.go") + `}`
	if got := pathvirtualization.ScanReservedAlias([]byte(prelude)); got != pathvirtualization.ReservedAliasWellFormed {
		t.Fatalf("scan over a %d-byte document=%v want well-formed", len(prelude), got)
	}
}

// TestScanReservedAliasPrefersTheStrongerSignal pins the aggregation rule: a payload
// that holds both a complete alias and a partial one reports the complete one, so a
// caller never sees a weaker label for a document that really does carry an alias.
func TestScanReservedAliasPrefersTheStrongerSignal(t *testing.T) {
	t.Parallel()

	malformed := "/.__lip_v1__/w_short/a.go"
	wellFormed := "/.__lip_v1__/w_" + scanTag + "/b.go"
	if got := pathvirtualization.ScanReservedAlias([]byte(malformed + " " + wellFormed)); got != pathvirtualization.ReservedAliasWellFormed {
		t.Fatalf("scan=%v want well-formed", got)
	}
	if got := pathvirtualization.ScanReservedAlias([]byte(wellFormed + " " + malformed)); got != pathvirtualization.ReservedAliasWellFormed {
		t.Fatalf("scan=%v want well-formed", got)
	}
	if got := pathvirtualization.ScanReservedAlias([]byte(malformed + " /home/dev/other/c.go")); got != pathvirtualization.ReservedAliasMalformedTag {
		t.Fatalf("scan=%v want malformed-tag", got)
	}
}

// TestScanReservedAliasRecognizesEscapedSpellings pins the closed answer for a
// payload that spells any part of the reserved marker with a JSON \u escape. The
// bytes contain no literal marker, so the literal scan alone cannot see the
// namespace the document actually denotes.
func TestScanReservedAliasRecognizesEscapedSpellings(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		raw  string
		want pathvirtualization.ReservedAliasPresence
	}{
		"fully escaped marker": {
			raw:  `{"file_path":"/\u002e\u005f\u005flip_v1__\u002fw_` + scanTag + `\u002fsrc\u002fa.go"`,
			want: pathvirtualization.ReservedAliasWellFormed,
		},
		"escaped underscore only": {
			raw:  `{"file_path":"/.\u005f\u005flip_v1__/w_` + scanTag + `/src/a.go"`,
			want: pathvirtualization.ReservedAliasWellFormed,
		},
		"literal marker still recognized": {
			raw:  `{"file_path":"/.__lip_v1__/w_` + scanTag + `/src/a.go"`,
			want: pathvirtualization.ReservedAliasWellFormed,
		},
		"escaped but wrong segment length": {
			raw:  `{"file_path":"/.\u005f\u005flip_v1__/w_short/src/a.go"}`,
			want: pathvirtualization.ReservedAliasMalformedTag,
		},
		"escaped marker inside a longer name": {
			raw:  `{"file_path":"/my.\u005flip_v1__x/src/a.go"`,
			want: pathvirtualization.ReservedAliasAbsent,
		},
		"no reserved namespace at all": {
			raw:  `{"file_path":"/usr/local/src/a.go"}`,
			want: pathvirtualization.ReservedAliasAbsent,
		},
		"truncated escape is inert": {
			raw:  `{"file_path":"/.\u005f"`,
			want: pathvirtualization.ReservedAliasAbsent,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := pathvirtualization.ScanReservedAlias([]byte(tc.raw)); got != tc.want {
				t.Errorf("ScanReservedAlias presence = %v, want %v", got, tc.want)
			}
		})
	}
}
