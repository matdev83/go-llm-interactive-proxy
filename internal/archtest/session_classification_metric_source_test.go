package archtest

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	featureclassification "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
)

// Task 11.2 (requirements 9.1-9.4, 9.6): the operator guide has to describe the
// observability surface, and a description an operator trusts has to be true.
//
// Nothing in this file restates the metric surface. Metric names, exported
// Prometheus types, and label sets are read out of the shipped collector with the
// Go parser; emission points are read out of the shipped observation structs; and
// every label value is read out of the shipped feature package. Renaming a
// metric, widening a label, or adding a family therefore breaks the contract
// instead of leaving stale prose behind in the guide.
//
// It parses the shipped sources rather than importing them because this is a
// documentation contract: what matters is the surface those files build, not a
// second compiled copy that could drift.

const sessionClassificationCollectorRel = "internal/standardplugins/featurehost/sessionclassification/metrics.go"

// sessionClassificationMetricFamily is one Prometheus family as the shipped
// collector actually builds it.
type sessionClassificationMetricFamily struct {
	// Name is the exported metric name from the shipped descriptor.
	Name string
	// Labels is the descriptor's exact variable-label list.
	Labels []string
	// Kind is the Prometheus type the collector exports the descriptor with.
	Kind string
	// Hooks are the bounded-observation entry points that feed this descriptor.
	Hooks []string
}

// Signature identifies a family by what an operator observes - its exported type
// plus its exact label set - without embedding any part of its name. Keying the
// expected vocabulary by signature means a renamed metric is never silently
// reclassified and a metric that gains a label cannot inherit the vocabulary of
// the family it used to resemble.
func (f sessionClassificationMetricFamily) Signature() string {
	labels := slices.Clone(f.Labels)
	slices.Sort(labels)
	return f.Kind + ":" + strings.Join(labels, ",")
}

// sessionClassificationLabelVocabularies binds each observed descriptor
// signature to the closed vocabulary that governs each of its labels. Every value
// comes from the shipped feature package, so a widened vocabulary must be
// documented or the guide fails.
var sessionClassificationLabelVocabularies = map[string]map[string][]string{
	"gauge:":               nil,
	"counter:mode,outcome": {"mode": featureclassification.ObservationModes(), "outcome": featureclassification.EvaluationOutcomes()},
	"counter:confidence,evidence,source": {
		// source and confidence are DERIVED at test time from the SDK's own
		// constants; see sessionClassificationStringEnumValues.
		"source":     nil,
		"confidence": nil,
		"evidence":   featureclassification.BoundedEvidenceCodes(),
	},
	"counter:outcome":           {"outcome": featureclassification.RemoteOutcomes()},
	"histogram:outcome":         {"outcome": featureclassification.RemoteOutcomes()},
	"counter:operation,outcome": {"operation": featureclassification.StoreOperations(), "outcome": featureclassification.StoreOutcomes()},
}

// sessionClassificationValueKinds maps the collector's Prometheus value
// constants to the type word an operator reads in a metric browser.
var sessionClassificationValueKinds = map[string]string{
	"CounterValue": "counter",
	"GaugeValue":   "gauge",
	"UntypedValue": "untyped",
}

// sessionClassificationRenderOnlyMethods read or reset collector state instead of
// feeding a family, so they are never an emission point.
var sessionClassificationRenderOnlyMethods = map[string]bool{
	"Collect":                true,
	"Describe":               true,
	"DroppedObservations":    true,
	"NewPrometheusCollector": true,
}

// parseSessionClassificationMetrics reads the shipped collector's descriptors,
// exported types, and emission points out of the shipped sources.
func parseSessionClassificationMetrics(t *testing.T) []sessionClassificationMetricFamily {
	t.Helper()
	file := sessionClassificationParseFile(t, sessionClassificationCollectorRel)
	observations := parseSessionClassificationObservations(t)

	descriptors := map[string]sessionClassificationMetricFamily{}
	kinds := map[string]string{}
	receiver := ""

	// First pass: every descriptor field the collector builds, and the Prometheus
	// value kind each descriptor is exported with.
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if fn.Recv != nil && len(fn.Recv.List) == 1 {
			receiver = fn.Recv.List[0].Names[0].Name
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			assigned, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			field, family, ok := sessionClassificationDescriptor(assigned)
			if !ok {
				return false
			}
			descriptors[field] = family
			return false
		})
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "prometheus" {
				return true
			}
			switch sel.Sel.Name {
			case "MustNewConstMetric":
				if len(call.Args) < 2 {
					return true
				}
				if field, ok := sessionClassificationDescField(call.Args[0], receiver); ok {
					if kind, ok := sessionClassificationValueKind(call.Args[1]); ok {
						kinds[field] = kind
					}
				}
			case "MustNewConstHistogram":
				if len(call.Args) >= 1 {
					if field, ok := sessionClassificationDescField(call.Args[0], receiver); ok {
						kinds[field] = "histogram"
					}
				}
			}
			return true
		})
	}
	if len(descriptors) == 0 {
		t.Fatalf("%s builds no prometheus.NewDesc family, so this contract cannot read the metric surface "+
			"and the guide's claim to document it cannot be verified", sessionClassificationCollectorRel)
	}

	// Second pass: which collector method writes state that Collect renders. A
	// label-free family has no observation struct behind it, so this is the only
	// way to name its emission point.
	rendered := sessionClassificationRenderedState(file)
	stateHooks := map[string]bool{}
	collectorMethods := sessionClassificationCollectorMethods(file)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if sessionClassificationRenderOnlyMethods[fn.Name.Name] || !collectorMethods[fn.Name.Name] {
			continue
		}
		for field := range sessionClassificationTouchedFields(fn, receiver) {
			if rendered[field] {
				stateHooks[fn.Name.Name] = true
			}
		}
	}

	labelFree := 0
	renderedHooks := sessionClassificationSortedKeys(stateHooks)
	families := make([]sessionClassificationMetricFamily, 0, len(descriptors))
	for field, family := range descriptors {
		kind, ok := kinds[field]
		if !ok {
			t.Fatalf("%s builds descriptor %s but never exports it with a Prometheus value kind; the guide "+
				"cannot document a type an operator could never observe", sessionClassificationCollectorRel, field)
		}
		family.Kind = kind
		family.Hooks = sessionClassificationObservationHooks(family, observations)
		if len(family.Hooks) == 0 {
			if len(family.Labels) != 0 {
				t.Fatalf("shipped family %q has labels %v that match no bounded observation struct, so this "+
					"contract cannot name an emission point for it", family.Name, family.Labels)
			}
			// A label-free family is rendered from collector state, so its emission
			// point is whichever method writes that state.
			family.Hooks = renderedHooks
			labelFree++
		}
		families = append(families, family)
	}
	if labelFree != 1 {
		t.Fatalf("%s exports %d label-free metric families; this contract resolves a label-free family's "+
			"emission point only while there is exactly one, so extend it before documenting a second",
			sessionClassificationCollectorRel, labelFree)
	}
	slices.SortFunc(families, func(left, right sessionClassificationMetricFamily) int {
		return strings.Compare(left.Name, right.Name)
	})
	return families
}

// sessionClassificationDescriptor reads one `field: prometheus.NewDesc(...)`
// descriptor assignment out of the parsed collector source.
func sessionClassificationDescriptor(assigned *ast.KeyValueExpr) (string, sessionClassificationMetricFamily, bool) {
	empty := sessionClassificationMetricFamily{}
	field, ok := assigned.Key.(*ast.Ident)
	if !ok {
		return "", empty, false
	}
	call, ok := assigned.Value.(*ast.CallExpr)
	if !ok || len(call.Args) < 3 {
		return "", empty, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "NewDesc" {
		return "", empty, false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "prometheus" {
		return "", empty, false
	}
	name, ok := sessionClassificationStringLiteral(call.Args[0])
	if !ok {
		return "", empty, false
	}
	labels, ok := sessionClassificationStringList(call.Args[2])
	if !ok {
		return "", empty, false
	}
	return field.Name, sessionClassificationMetricFamily{Name: name, Labels: labels}, true
}

func sessionClassificationDescField(expr ast.Expr, receiver string) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != receiver {
		return "", false
	}
	return sel.Sel.Name, true
}

func sessionClassificationValueKind(expr ast.Expr) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "prometheus" {
		return "", false
	}
	kind, ok := sessionClassificationValueKinds[sel.Sel.Name]
	return kind, ok
}

func sessionClassificationStringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// sessionClassificationStringList reads a `nil` or `[]string{...}` descriptor
// label argument. A dynamically built label list would defeat this contract, so
// it is reported as unreadable rather than silently skipped.
func sessionClassificationStringList(expr ast.Expr) ([]string, bool) {
	switch node := expr.(type) {
	case *ast.Ident:
		if node.Name == "nil" {
			return nil, true
		}
		return nil, false
	case *ast.CompositeLit:
		values := make([]string, 0, len(node.Elts))
		for _, element := range node.Elts {
			value, ok := sessionClassificationStringLiteral(element)
			if !ok {
				return nil, false
			}
			values = append(values, value)
		}
		return values, true
	default:
		return nil, false
	}
}

func sessionClassificationParseFile(t *testing.T, rel string) *ast.File {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(rel))
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return file
}

// sessionClassificationStringEnumValues enumerates every constant of a named
// string-typed kind in a package, read out of the shipped source. The `source` and
// `confidence` label vocabularies have no shipped accessor, and hand-writing them
// made a widened vocabulary pass silently while the operator guide went stale - so
// they are derived here from the SDK's own declarations instead.
//
// Deriving from the TYPE rather than from a literal list is what makes this a
// ratchet: adding ConfidenceMedium to pkg/lipsdk/session changes the answer here,
// and the doc contract then fails until the guide is corrected.
func sessionClassificationStringEnumValues(t *testing.T, importPath, typeName string) []string {
	t.Helper()

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  repoRoot(t),
	}
	loaded, err := packages.Load(cfg, importPath)
	if err != nil {
		t.Fatalf("load %s: %v", importPath, err)
	}
	if len(loaded) != 1 {
		t.Fatalf("%s loaded %d packages, want 1", importPath, len(loaded))
	}
	scope := loaded[0].Types.Scope()
	named, ok := scope.Lookup(typeName).Type().(*types.Named)
	if !ok {
		t.Fatalf("%s does not declare a named type %q, so its vocabulary cannot be derived", importPath, typeName)
	}
	basic, ok := named.Underlying().(*types.Basic)
	if !ok || basic.Kind() != types.String {
		t.Fatalf("%s.%s is not a string kind, so enumerating its constants would not bound a label value", importPath, typeName)
	}

	var values []string
	for _, name := range scope.Names() {
		object, ok := scope.Lookup(name).(*types.Const)
		if !ok || object.Type() != named {
			continue
		}
		// The all-zero sentinel (ConfidenceUnknown = "") is the absence of a band,
		// not a band an observation can carry: a transition label is never empty.
		// Publishing it would assert an unobservable label value.
		if value := constant.StringVal(object.Val()); value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		t.Fatalf("%s.%s has no constants; the guide would claim an empty vocabulary", importPath, typeName)
	}
	slices.Sort(values)
	return values
}
