package rewrite

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
)

// The document-level tests exercise the surface-splice engine directly, because the
// byte-preservation contract is a property of that engine and not visible through
// a canonical call alone.
const (
	docRoot   = "/home/dev/projects/go-llm-interactive-proxy"
	docAlias  = "/.__lip_v1__/w_ylfucd77chy74zh3qwma/"
	docTarget = docRoot + "/pkg/lipapi/call.go"
	docVPath  = docAlias + "pkg/lipapi/call.go"
)

func docMapping(t *testing.T) pathvirtualization.Mapping {
	t.Helper()

	mapping, reason := pathvirtualization.DeriveMapping(docRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping(%q) reason = %q, want none", docRoot, reason)
	}
	return mapping
}

func docPointers(t *testing.T, pointers ...string) pathvirtualization.SelectorSet {
	t.Helper()

	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{"engine"},
		ArgPointers: pointers,
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileProfiles reject = %q, want none", reject)
	}
	return compiled[0].ArgPointers
}

// TestRewriteDocumentSplicesOnlySelectedLiterals proves the engine replaces the
// exact bytes of a selected string literal and nothing else, for every JSON spacing
// and escaping shape a client can send.
func TestRewriteDocumentSplicesOnlySelectedLiterals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		document  string
		pointers  []string
		want      string
		eligible  int
		rewritten int
	}{
		{
			name:      "compact",
			document:  `{"file_path":"` + docTarget + `"}`,
			pointers:  []string{"/file_path"},
			want:      `{"file_path":"` + docVPath + `"}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "spaced",
			document:  "{ \"file_path\" : \t\"" + docTarget + "\"\n }",
			pointers:  []string{"/file_path"},
			want:      "{ \"file_path\" : \t\"" + docVPath + "\"\n }",
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "member_order_preserved",
			document:  `{"z":1,"file_path":"` + docTarget + `","a":2}`,
			pointers:  []string{"/file_path"},
			want:      `{"z":1,"file_path":"` + docVPath + `","a":2}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "nested_object_path",
			document:  `{"op":{"file_path":"` + docTarget + `","mode":"r"}}`,
			pointers:  []string{"/op/file_path"},
			want:      `{"op":{"file_path":"` + docVPath + `","mode":"r"}}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "string_array",
			document:  `{"paths":["` + docTarget + `","` + docRoot + `/pkg/lipapi/items.go","relative.go"]}`,
			pointers:  []string{"/paths"},
			want:      `{"paths":["` + docVPath + `","` + docAlias + `pkg/lipapi/items.go","relative.go"]}`,
			eligible:  2,
			rewritten: 2,
		},
		{
			name:      "single_array_element",
			document:  `{"paths":["` + docTarget + `","` + docRoot + `/pkg/lipapi/items.go"]}`,
			pointers:  []string{"/paths/1"},
			want:      `{"paths":["` + docTarget + `","` + docAlias + `pkg/lipapi/items.go"]}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "array_of_arrays_is_not_a_string_array",
			document:  `{"paths":[["` + docTarget + `"]]}`,
			pointers:  []string{"/paths"},
			want:      `{"paths":[["` + docTarget + `"]]}`,
			eligible:  0,
			rewritten: 0,
		},
		{
			name:      "object_value_is_never_descended",
			document:  `{"file_path":{"deep":{"file_path":"` + docTarget + `"}}}`,
			pointers:  []string{"/file_path"},
			want:      `{"file_path":{"deep":{"file_path":"` + docTarget + `"}}}`,
			eligible:  0,
			rewritten: 0,
		},
		{
			name:      "escaped_member_name",
			document:  `{"a/b":"` + docTarget + `","c~d":"` + docTarget + `"}`,
			pointers:  []string{"/a~1b"},
			want:      `{"a/b":"` + docVPath + `","c~d":"` + docTarget + `"}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "tilde_member_name",
			document:  `{"c~d":"` + docTarget + `"}`,
			pointers:  []string{"/c~0d"},
			want:      `{"c~d":"` + docVPath + `"}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "empty_object",
			document:  `{}`,
			pointers:  []string{"/file_path"},
			want:      `{}`,
			eligible:  0,
			rewritten: 0,
		},
		{
			name:      "unselected_leaf_holding_the_real_root",
			document:  `{"content":"` + docRoot + `/a.go","patch":"` + docRoot + `"}`,
			pointers:  []string{"/file_path"},
			want:      `{"content":"` + docRoot + `/a.go","patch":"` + docRoot + `"}`,
			eligible:  0,
			rewritten: 0,
		},
		{
			name:      "unicode_value",
			document:  `{"file_path":"` + docTarget + `/ünïcødé/文件.go"}`,
			pointers:  []string{"/file_path"},
			want:      `{"file_path":"` + docVPath + `/ünïcødé/文件.go"}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "escape_sequences_in_value",
			document:  `{"file_path":"` + docTarget + `/a\"b\\c\nd.go"}`,
			pointers:  []string{"/file_path"},
			want:      `{"file_path":"` + docVPath + `/a\"b\\c\nd.go"}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "unselected_sibling_holding_the_real_root",
			document:  `{"file_path":"` + docTarget + `","note":"` + docRoot + `"}`,
			pointers:  []string{"/file_path"},
			want:      `{"file_path":"` + docVPath + `","note":"` + docRoot + `"}`,
			eligible:  1,
			rewritten: 1,
		},
		{
			name:      "two_selected_leaves",
			document:  `{"file_path":"` + docTarget + `","workdir":"` + docRoot + `"}`,
			pointers:  []string{"/file_path", "/workdir"},
			want:      `{"file_path":"` + docVPath + `","workdir":"` + docAlias + `"}`,
			eligible:  2,
			rewritten: 2,
		},
		{
			name:      "same_leaf_selected_twice_counts_once",
			document:  `{"paths":["` + docTarget + `"]}`,
			pointers:  []string{"/paths", "/paths/0"},
			want:      `{"paths":["` + docVPath + `"]}`,
			eligible:  1,
			rewritten: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rewriter := New(docMapping(t), nil)
			var acc account
			got, changed, err := rewriter.rewriteDocument([]byte(tc.document), docPointers(t, tc.pointers...), &acc)
			if err != nil {
				t.Fatalf("rewriteDocument: %v", err)
			}
			if changed != (tc.rewritten > 0) {
				t.Errorf("changed = %v, want %v", changed, tc.rewritten > 0)
			}
			if !changed {
				if !reflect.DeepEqual(got, []byte(nil)) {
					t.Errorf("unchanged document published bytes: %s", got)
				}
				return
			}
			if string(got) != tc.want {
				t.Errorf("document bytes:\ngot:  %s\nwant: %s", got, tc.want)
			}
			if !json.Valid(got) {
				t.Errorf("result is not valid JSON: %s", got)
			}
			if acc.eligible != tc.eligible || acc.rewritten != tc.rewritten {
				t.Errorf("account eligible/rewritten = %d/%d, want %d/%d", acc.eligible, acc.rewritten, tc.eligible, tc.rewritten)
			}
		})
	}
}

// TestRewriteDocumentRefusesUnusablePayloads proves the engine reports a bounded
// reason and returns no bytes for every payload class it cannot treat as a
// complete argument object.
func TestRewriteDocumentRefusesUnusablePayloads(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		document   string
		wantReason SkipReason
	}{
		{name: "absent", document: ``, wantReason: SkipReasonPayloadAbsent},
		{name: "null", document: `null`, wantReason: SkipReasonPayloadAbsent},
		{name: "array_root", document: `["` + docTarget + `"]`, wantReason: SkipReasonPayloadNotObject},
		{name: "string_root", document: `"` + docTarget + `"`, wantReason: SkipReasonPayloadNotObject},
		{name: "number_root", document: `12`, wantReason: SkipReasonPayloadNotObject},
		{name: "bool_root", document: `true`, wantReason: SkipReasonPayloadNotObject},
		{name: "truncated", document: `{"a":`, wantReason: SkipReasonPayloadInvalid},
		{name: "trailing_value", document: `{"a":1} {"b":2}`, wantReason: SkipReasonPayloadInvalid},
		{name: "trailing_garbage", document: `{"a":1} x`, wantReason: SkipReasonPayloadInvalid},
		{name: "not_json", document: `a=b`, wantReason: SkipReasonPayloadInvalid},
		{name: "unterminated_string", document: `{"a":"b`, wantReason: SkipReasonPayloadInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var acc account
			got, changed, err := New(docMapping(t), nil).rewriteDocument([]byte(tc.document), docPointers(t, "/file_path"), &acc)
			if err != nil {
				t.Fatalf("rewriteDocument: %v", err)
			}
			if changed || got != nil {
				t.Errorf("refused payload produced bytes: %s", got)
			}
			if acc.skips[tc.wantReason] != 1 {
				t.Errorf("account skips = %+v, want one %q", acc.skips, tc.wantReason)
			}
		})
	}
}

// TestRewriteDocumentSelectorReasonsAreProjected proves every bounded selector
// refusal the engine can observe is reported under its own reason, so a later
// accounting stage never has to guess why a selected location contributed nothing.
func TestRewriteDocumentSelectorReasonsAreProjected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		document   string
		pointer    string
		wantReason SkipReason
	}{
		{name: "unresolved", document: `{"other":1}`, pointer: "/file_path", wantReason: SkipReasonSelectorUnresolved},
		{name: "below_scalar", document: `{"file_path":"/` + docRoot + `"}`, pointer: "/file_path/deeper", wantReason: SkipReasonSelectorUnresolved},
		{name: "array_end_token", document: `{"paths":["a"]}`, pointer: "/paths/1", wantReason: SkipReasonSelectorUnresolved},
		{name: "object", document: `{"file_path":{}}`, pointer: "/file_path", wantReason: SkipReasonSelectorObject},
		{name: "number", document: `{"file_path":1}`, pointer: "/file_path", wantReason: SkipReasonSelectorNotString},
		{name: "null", document: `{"file_path":null}`, pointer: "/file_path", wantReason: SkipReasonSelectorNotString},
		{name: "bool", document: `{"file_path":true}`, pointer: "/file_path", wantReason: SkipReasonSelectorNotString},
		{name: "mixed_array", document: `{"file_path":["a",1]}`, pointer: "/file_path", wantReason: SkipReasonSelectorNotStringArray},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var acc account
			if _, changed, err := New(docMapping(t), nil).rewriteDocument([]byte(tc.document), docPointers(t, tc.pointer), &acc); err != nil {
				t.Fatalf("rewriteDocument: %v", err)
			} else if changed {
				t.Fatal("refused selector rewrote bytes")
			}
			if acc.skips[tc.wantReason] != 1 {
				t.Errorf("account skips = %+v, want one %q", acc.skips, tc.wantReason)
			}
		})
	}
}

// TestIsAbsentOrNullPayloadMatchesCanonicalPresence proves the local presence helper
// is exact: it never trims, and it never mistakes a quoted "null" for the literal.
// The feature cannot import internal/core, so this is the local equivalent of
// internal/core/jsonpresence.IsAbsentOrJSONNull and is pinned here on purpose.
func TestIsAbsentOrNullPayloadMatchesCanonicalPresence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw  string
		want bool
	}{
		{raw: ``, want: true},
		{raw: `null`, want: true},
		{raw: ` null `, want: false},
		{raw: `nul`, want: false},
		{raw: `{}`, want: false},
		{raw: `[]`, want: false},
		{raw: `"null"`, want: false},
		{raw: `0`, want: false},
	}
	for _, tc := range cases {
		if got := isAbsentOrNullPayload([]byte(tc.raw)); got != tc.want {
			t.Errorf("isAbsentOrNullPayload(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// TestSelectorSkipReasonProjectionIsTotal proves the projection from the canonical
// selector vocabulary to this step's bounded vocabulary is total: every selector
// reason maps to exactly one reason here, and the no-skip value never becomes a
// recorded skip.
func TestSelectorSkipReasonProjectionIsTotal(t *testing.T) {
	t.Parallel()

	projected := map[pathvirtualization.SelectorSkip]SkipReason{
		pathvirtualization.SelectorSkipNone:           SkipReasonNone,
		pathvirtualization.SelectorSkipUnresolved:     SkipReasonSelectorUnresolved,
		pathvirtualization.SelectorSkipNotString:      SkipReasonSelectorNotString,
		pathvirtualization.SelectorSkipObject:         SkipReasonSelectorObject,
		pathvirtualization.SelectorSkipNotStringArray: SkipReasonSelectorNotStringArray,
	}
	for skip := pathvirtualization.SelectorSkip(0); skip < 16; skip++ {
		want, known := projected[skip]
		got := selectorSkipReason(skip)
		if !known {
			if got != SkipReasonNone {
				t.Errorf("selector skip %v projected to %v, want no recorded reason", skip, got)
			}
			continue
		}
		if got != want {
			t.Errorf("selector skip %v projected to %v, want %v", skip, got, want)
		}
	}
}

// TestFindSelectedStringSpansIsByteExact proves the offset arithmetic that makes the
// splice safe: every recorded span is a complete JSON string literal at the position
// the decoded document reports, for nested, spaced, and escaped layouts.
func TestFindSelectedStringSpansIsByteExact(t *testing.T) {
	t.Parallel()

	document := []byte("{\n  \"a\" : [ \"one\" , \"two\" ] ,\n  \"b/c\" : \"three\" ,\n  \"d\" : { \"e\" : \"four\" }\n}")
	keys := map[string]struct{}{
		"/a/0":    {},
		"/a/1":    {},
		"/b~1c":   {},
		"/d/e":    {},
		"/b/c":    {}, // the unescaped spelling must not match
		"/absent": {},
	}
	spans, err := findSelectedStringSpans(document, keys)
	if err != nil {
		t.Fatalf("findSelectedStringSpans: %v", err)
	}
	got := make([]string, 0, len(spans))
	for _, span := range spans {
		if span.start < 0 || span.end > len(document) || span.start >= span.end {
			t.Fatalf("span [%d,%d) is out of range for %d bytes", span.start, span.end, len(document))
		}
		var decoded string
		if err := json.Unmarshal(document[span.start:span.end], &decoded); err != nil {
			t.Fatalf("span [%d,%d) is not a JSON string literal: %q", span.start, span.end, document[span.start:span.end])
		}
		if decoded != span.value {
			t.Errorf("span value %q does not match its bytes %q", span.value, document[span.start:span.end])
		}
		got = append(got, span.value)
	}
	sort.Strings(got)
	if want := []string{"four", "one", "three", "two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %v, want %v", got, want)
	}
}

// TestRewriteDocumentLeavesNumbersAndEscapesIntact proves the engine never
// re-encodes a payload: number spelling, escapes, and member order survive, and the
// result is always a valid document.
func TestRewriteDocumentLeavesNumbersAndEscapesIntact(t *testing.T) {
	t.Parallel()

	document := `{"file_path":"` + docRoot + `/x.go","big":12345678901234567890,"exp":1.5e+300,"neg":-0.0,"esc":"a\u00e9b\ud83d\ude00","uni":"héllo","sur":"\ud800","dup":1,"dup":2}`
	var acc account
	got, changed, err := New(docMapping(t), nil).rewriteDocument([]byte(document), docPointers(t, "/file_path"), &acc)
	if err != nil {
		t.Fatalf("rewriteDocument: %v", err)
	}
	if !changed {
		t.Fatal("expected a rewrite")
	}
	want := `{"file_path":"` + docAlias + `x.go","big":12345678901234567890,"exp":1.5e+300,"neg":-0.0,"esc":"a\u00e9b\ud83d\ude00","uni":"héllo","sur":"\ud800","dup":1,"dup":2}`
	if string(got) != want {
		t.Errorf("document bytes:\ngot:  %s\nwant: %s", got, want)
	}
	if !json.Valid(got) {
		t.Errorf("result is not valid JSON: %s", got)
	}
}

// TestCanonicalPointerTextIsInjective proves the pointer-text key the engine matches
// against is the RFC 6901 canonical spelling, so two different member names can
// never collide on one key.
func TestCanonicalPointerTextIsInjective(t *testing.T) {
	t.Parallel()

	names := []string{"a", "a/b", "a~b", "a~0b", "a~1b", "", "0"}
	seen := make(map[string]string, len(names))
	for _, name := range names {
		key := "/" + canonicalPointerText(name)
		if previous, duplicate := seen[key]; duplicate {
			t.Errorf("member names %q and %q collide on %q", previous, name, key)
		}
		seen[key] = name
	}
}

// TestTokenStartSkipsOnlySeparators proves the offset helper advances over exactly
// the structural bytes between two tokens and never past a value token's first byte.
func TestTokenStartSkipsOnlySeparators(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		document string
		from     int
		want     int
	}{
		{name: "member_separator", document: `{"a":"b"}`, from: 4, want: 5},
		{name: "array_separator", document: `["a","b"]`, from: 4, want: 5},
		{name: "leading_whitespace", document: "{\n\t\"a\"", from: 1, want: 3},
		{name: "no_separators", document: `["a"]`, from: 1, want: 1},
		{name: "colon_then_spaces", document: `{"a" :  "b"}`, from: 5, want: 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tokenStart([]byte(tc.document), tc.from); got != tc.want {
				t.Errorf("tokenStart(%q, %d) = %d, want %d", tc.document, tc.from, got, tc.want)
			}
		})
	}
}

// TestTokenStartNeverRunsPastTheDocument proves the helper cannot index out of range
// even when the previous offset already sits at the end of the payload.
func TestTokenStartNeverRunsPastTheDocument(t *testing.T) {
	t.Parallel()

	for _, document := range []string{"", " ", ",", ":", "::,,"} {
		if got := tokenStart([]byte(document), len(document)); got != len(document) {
			t.Errorf("tokenStart(%q, %d) = %d, want %d", document, len(document), got, len(document))
		}
		if got := tokenStart([]byte(document), len(document)+8); got < 0 {
			t.Errorf("tokenStart(%q, past end) = %d, want a clamped offset", document, got)
		}
	}
}

// TestSpanReplacementNeverOverlaps proves the splice applies replacements from the
// end of the payload backwards, so no replacement can invalidate another's offset.
func TestSpanReplacementNeverOverlaps(t *testing.T) {
	t.Parallel()

	document := []byte(`{"paths":["` + docTarget + `","` + docRoot + `/pkg/lipapi/items.go","relative"]}`)
	var acc account
	got, changed, err := New(docMapping(t), nil).rewriteDocument(document, docPointers(t, "/paths"), &acc)
	if err != nil {
		t.Fatalf("rewriteDocument: %v", err)
	}
	if !changed {
		t.Fatal("expected a rewrite")
	}
	var decoded struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !reflect.DeepEqual(decoded.Paths, []string{docVPath, docAlias + "pkg/lipapi/items.go", "relative"}) {
		t.Errorf("paths = %v", decoded.Paths)
	}
	if acc.eligible != 2 || acc.rewritten != 2 {
		t.Errorf("account eligible/rewritten = %d/%d, want 2/2", acc.eligible, acc.rewritten)
	}
	if acc.bytesBefore <= acc.bytesAfter {
		t.Errorf("account recorded no savings: before=%d after=%d", acc.bytesBefore, acc.bytesAfter)
	}
}

// TestEncodeSelectedValueEscapesReplacement proves the replacement literal is always
// a valid JSON string, whatever bytes a client's path carries.
func TestEncodeSelectedValueEscapesReplacement(t *testing.T) {
	t.Parallel()

	for _, value := range []string{`/plain`, "/quote\"and\\slash", "tab\there", "<a>&amp;</a>", "line\nbreak", "\x00\x01"} {
		encoded, err := encodeSelectedValue(value)
		if err != nil {
			t.Fatalf("encodeSelectedValue(%q): %v", value, err)
		}
		var decoded string
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("encoded %q is not a JSON string: %v", encoded, err)
		}
		if decoded != value {
			t.Errorf("round trip changed %q to %q", value, decoded)
		}
	}
}

// TestAccountEmitsReasonsInFixedOrder proves the statistics carry one entry per
// reason, in ascending reason order, with no entry for a reason that never happened.
func TestAccountEmitsReasonsInFixedOrder(t *testing.T) {
	t.Parallel()

	var acc account
	acc.skip(SkipReasonSelectorUnresolved)
	acc.skip(SkipReasonSelectorUnresolved)
	acc.skip(SkipReasonPayloadAbsent)
	acc.eligible = 3
	acc.rewritten = 2
	acc.bytesBefore = 100
	acc.bytesAfter = 40

	stats := acc.stats()
	want := []Skip{
		{Reason: SkipReasonPayloadAbsent, Count: 1},
		{Reason: SkipReasonSelectorUnresolved, Count: 2},
	}
	if !reflect.DeepEqual(stats.Skips, want) {
		t.Errorf("stats skips = %+v, want %+v", stats.Skips, want)
	}
	if stats.Eligible != 3 || stats.Rewritten != 2 || stats.BytesBefore != 100 || stats.BytesAfter != 40 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.BytesSaved() != 60 {
		t.Errorf("BytesSaved = %d, want 60", stats.BytesSaved())
	}
	if acc.stats().BytesSaved() != 60 {
		t.Error("BytesSaved must be derived, so it cannot drift from its inputs")
	}
}

// TestStatsBytesSavedIsDerived proves the derived field cannot disagree with the two
// byte totals it is defined from, including for a rewrite that grew a payload.
func TestStatsBytesSavedIsDerived(t *testing.T) {
	t.Parallel()

	cases := []Stats{
		{},
		{BytesBefore: 10, BytesAfter: 4},
		{BytesBefore: 4, BytesAfter: 10},
		{BytesBefore: 1 << 30, BytesAfter: 1},
	}
	for _, stats := range cases {
		if got, want := stats.BytesSaved(), stats.BytesBefore-stats.BytesAfter; got != want {
			t.Errorf("BytesSaved() = %d, want %d", got, want)
		}
	}
}

// TestStringArraySelectionIsAllOrNothingAtDocumentLevel proves a mixed array refuses
// every element even when a sibling string array in the same payload is valid.
func TestStringArraySelectionIsAllOrNothingAtDocumentLevel(t *testing.T) {
	t.Parallel()

	document := `{"paths":["` + docTarget + `",7],"other":["` + docTarget + `"]}`
	var acc account
	got, changed, err := New(docMapping(t), nil).rewriteDocument([]byte(document), docPointers(t, "/paths", "/other"), &acc)
	if err != nil {
		t.Fatalf("rewriteDocument: %v", err)
	}
	if !changed {
		t.Fatal("expected the valid array to be rewritten")
	}
	var decoded struct {
		Paths []any    `json:"paths"`
		Other []string `json:"other"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if decoded.Paths[0] != docTarget {
		t.Errorf("refused mixed array was partially rewritten: %v", decoded.Paths)
	}
	if decoded.Other[0] != docVPath {
		t.Errorf("valid sibling array not rewritten: %v", decoded.Other)
	}
	if acc.skips[SkipReasonSelectorNotStringArray] != 1 {
		t.Errorf("account skips = %+v, want one selector_not_string_array", acc.skips)
	}
}

// TestAccountIndexIsBounded proves the reason tally cannot grow past the closed
// vocabulary, so a hostile payload cannot inflate the accounting structure.
func TestAccountIndexIsBounded(t *testing.T) {
	t.Parallel()

	var acc account
	for range 1000 {
		acc.skip(SkipReasonSelectorUnresolved)
		acc.skip(SkipReason(250))
	}
	stats := acc.stats()
	if len(stats.Skips) != 1 {
		t.Errorf("account published %d reasons, want 1: %+v", len(stats.Skips), stats.Skips)
	}
	if stats.Skips[0] != (Skip{Reason: SkipReasonSelectorUnresolved, Count: 1000}) {
		t.Errorf("account = %+v, want 1000 selector_unresolved", stats.Skips[0])
	}
	if stats.BytesSaved() != 0 {
		t.Error("account without byte totals must report zero savings")
	}
}
