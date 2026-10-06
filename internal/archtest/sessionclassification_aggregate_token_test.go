package archtest

// Task 12.1 certification assertion for the aggregate token this task owns.
//
// Requirement 11.4 / 12.11 inertness: archForbiddenFeatureTokens must reject a
// field named or typed after the session classifier on every guarded generic
// aggregate. The live tree is clean, which is what makes the guard correct
// rather than blocking, so a passing live sweep cannot by itself show the token
// is load-bearing. The token's effectiveness is therefore proven here with
// synthetic fixtures: without them, an inert or dead entry would look identical
// to a working one.
//
// The companion half of the claim is
// TestGenericAggregatesContainNoPerFeatureFields, which proves the token is
// inert against the real aggregates today.

import (
	"go/parser"
	"go/token"
	"testing"
)

// TestSessionClassificationTokenRejectsGuardedAggregateFields is the proof that
// "sessionclassification" is an active entry in archForbiddenFeatureTokens, so a
// guarded generic aggregate cannot acquire an SDK-classifier field by name or by
// type.
//
// Both spellings are exercised because the scanner tests them separately: a
// field NAME carrying the token, and a field TYPE carrying it. The type case is
// the one that matters most, because pkg/lipsdk/sessionclassification is a legal
// import for the sanctioned generic-core seam
// (internal/core/extensions/session_classification.go); naming the type is what
// would smuggle a classifier into an aggregate.
func TestSessionClassificationTokenRejectsGuardedAggregateFields(t *testing.T) {
	t.Parallel()

	const classifierImport = `"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"`

	cases := []struct {
		name       string
		src        string
		exceptions map[string]string
		want       int
	}{
		{
			name: "field name carrying the token is rejected",
			src: "package fixture\n" +
				"type SyntheticAggregate struct {\n" +
				"\tSessionClassification any\n" +
				"}\n",
			want: 1,
		},
		{
			name: "SDK classifier field type is rejected",
			src: "package fixture\n" +
				"import " + classifierImport + "\n" +
				"type SyntheticAggregate struct {\n" +
				"\tClassifier sessionclassification.Classifier\n" +
				"}\n",
			want: 1,
		},
		{
			name: "nested same-package group cannot hide the classifier",
			src: "package fixture\n" +
				"import " + classifierImport + "\n" +
				"type SyntheticAggregate struct {\n" +
				"\tNested struct {\n" +
				"\t\tClassifier sessionclassification.Classifier\n" +
				"\t}\n" +
				"}\n",
			want: 1,
		},
		{
			name: "an approval claiming another type cannot silence the token",
			src: "package fixture\n" +
				"import " + classifierImport + "\n" +
				"type SyntheticAggregate struct {\n" +
				"\tClassifier sessionclassification.Classifier\n" +
				"}\n",
			exceptions: map[string]string{"Classifier": "int"},
			want:       1,
		},
		{
			name: "control: unrelated generic fields stay silent",
			src: "package fixture\n" +
				"type SyntheticAggregate struct {\n" +
				"\tUserAgent string\n" +
				"}\n",
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			node, err := parser.ParseFile(fset, "synthetic.go", tc.src, 0)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			got := scanStructForFeatureFields(node, "SyntheticAggregate", tc.exceptions)
			if len(got) != tc.want {
				t.Fatalf("scan reported %d violations, want %d: %v", len(got), tc.want, got)
			}
		})
	}
}
