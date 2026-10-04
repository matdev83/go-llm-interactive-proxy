package archtest

import (
	"go/ast"
	"slices"
	"strings"
	"testing"
)

// Task 11.2 emission-point derivation. A metric family's labels and its feeding
// entry point are two views of the same bounded observation contract, so both are
// read out of the shipped feature sources rather than restated: a family's label
// set is exactly the closed-vocabulary fields of the observation struct that feeds
// it, and the method accepting that struct is the emission point an operator-facing
// guide should name.
//
// Recognising the contract structurally - the one interface whose every method is
// named `Observe*` and takes a single observation struct - means a rename cannot
// silently repoint this derivation at a different interface.

const sessionClassificationObservabilityRel = "internal/plugins/features/sessionclassification/observability.go"

// sessionClassificationObservation is one bounded observation the feature's
// observer contract accepts.
type sessionClassificationObservation struct {
	// Method is the observer method name an operator-facing guide names.
	Method string
	// Type is the observation struct the method accepts.
	Type string
	// Labels are the struct's closed-vocabulary fields, lowercased. A field whose
	// type is a measured scalar is excluded: those carry a sample or a counter,
	// never a label.
	Labels []string
}

type sessionClassificationObserverMethod struct {
	Method string
	Type   string
}

// sessionClassificationMutexCalls are the lock calls that are never a published
// value.
var sessionClassificationMutexCalls = map[string]bool{
	"Lock": true, "Unlock": true, "RLock": true, "RUnlock": true, "TryLock": true,
}

// sessionClassificationScalarTypes are the field types that can hold one
// published value. A map, a descriptor pointer, or a mutex holds no value of its
// own, so a field of one of those types is never a sample an operator sees.
var sessionClassificationScalarTypes = map[string]bool{
	"bool": true, "int": true, "int64": true, "uint": true, "uint64": true,
	"float64": true, "string": true,
}

// sessionClassificationObservations reads the feature's observer contract: one
// bounded observation struct per method, with the fields that can become labels.
func parseSessionClassificationObservations(t *testing.T) []sessionClassificationObservation {
	t.Helper()
	file := sessionClassificationParseFile(t, sessionClassificationObservabilityRel)

	structs := map[string]map[string]bool{}
	contracts := []*ast.InterfaceType{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			switch declared := typeSpec.Type.(type) {
			case *ast.StructType:
				labels := map[string]bool{}
				for _, field := range declared.Fields.List {
					if len(field.Names) != 1 || sessionClassificationMeasuredScalar(field.Type) {
						continue
					}
					labels[strings.ToLower(field.Names[0].Name)] = true
				}
				structs[typeSpec.Name.Name] = labels
			case *ast.InterfaceType:
				contracts = append(contracts, declared)
			}
		}
	}

	var observations []sessionClassificationObservation
	for _, iface := range contracts {
		methods, ok := sessionClassificationObserverContract(iface)
		if !ok {
			continue
		}
		for _, method := range methods {
			labels, known := structs[method.Type]
			if !known {
				t.Fatalf("%s declares observer method %s(%s) whose observation struct is not defined in the "+
					"same file", sessionClassificationObservabilityRel, method.Method, method.Type)
			}
			observations = append(observations, sessionClassificationObservation{
				Method: method.Method,
				Type:   method.Type,
				Labels: sessionClassificationSortedKeys(labels),
			})
		}
	}
	if len(observations) == 0 {
		t.Fatalf("%s declares no bounded observation contract, so this contract cannot tell which entry point "+
			"feeds which metric family", sessionClassificationObservabilityRel)
	}
	return observations
}

// sessionClassificationObserverContract recognises the one interface in the
// feature whose every method is named `Observe*` and takes a single observation
// struct.
func sessionClassificationObserverContract(iface *ast.InterfaceType) ([]sessionClassificationObserverMethod, bool) {
	var methods []sessionClassificationObserverMethod
	for _, field := range iface.Methods.List {
		if len(field.Names) != 1 || !strings.HasPrefix(field.Names[0].Name, "Observe") || field.Type == nil {
			return nil, false
		}
		fn, ok := field.Type.(*ast.FuncType)
		if !ok || fn.Params == nil || len(fn.Params.List) != 1 || len(fn.Params.List[0].Names) > 1 {
			return nil, false
		}
		ident, ok := fn.Params.List[0].Type.(*ast.Ident)
		if !ok {
			return nil, false
		}
		methods = append(methods, sessionClassificationObserverMethod{
			Method: field.Names[0].Name,
			Type:   ident.Name,
		})
	}
	return methods, len(methods) > 0
}

// sessionClassificationMeasuredScalar reports whether a struct field carries a
// measured sample rather than a closed-vocabulary label. `time.Duration` is a
// histogram sample and `uint64` is an unbounded counter such as a revision;
// neither may be reasoned about by type alone, so both are excluded by type.
func sessionClassificationMeasuredScalar(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name == "uint64"
	case *ast.SelectorExpr:
		pkg, ok := node.X.(*ast.Ident)
		return ok && pkg.Name == "time" && node.Sel.Name == "Duration"
	default:
		return false
	}
}

// sessionClassificationObservationHooks resolves the bounded-observation entry
// points that feed one family: exactly the observation struct whose
// closed-vocabulary fields are that descriptor's label set. The comparison is
// order-insensitive because a descriptor's label order is a rendering choice.
func sessionClassificationObservationHooks(family sessionClassificationMetricFamily, observations []sessionClassificationObservation) []string {
	labels := slices.Clone(family.Labels)
	slices.Sort(labels)
	var hooks []string
	for _, observation := range observations {
		candidate := slices.Clone(observation.Labels)
		slices.Sort(candidate)
		if slices.Equal(candidate, labels) {
			hooks = append(hooks, observation.Method)
		}
	}
	slices.Sort(hooks)
	return slices.Compact(hooks)
}

// sessionClassificationCollectorMethods returns every method the shipped
// collector declares on its pointer receiver.
func sessionClassificationCollectorMethods(file *ast.File) map[string]bool {
	methods := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if ident, ok := star.X.(*ast.Ident); ok && ident.Name == "PrometheusCollector" {
			methods[fn.Name.Name] = true
		}
	}
	return methods
}

// sessionClassificationCollectorScalars returns the shipped collector's
// single-value state fields.
func sessionClassificationCollectorScalars(file *ast.File) map[string]bool {
	scalars := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "PrometheusCollector" {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structType.Fields.List {
				ident, ok := field.Type.(*ast.Ident)
				if !ok || len(field.Names) != 1 || !sessionClassificationScalarTypes[ident.Name] {
					continue
				}
				scalars[field.Names[0].Name] = true
			}
		}
	}
	return scalars
}

// sessionClassificationRenderedState returns the collector state fields that
// Collect actually renders as a published value. Mutex traffic is skipped
// because a lock is how Collect synchronises, never what it publishes, and only
// single-value fields qualify.
func sessionClassificationRenderedState(file *ast.File) map[string]bool {
	rendered := map[string]bool{}
	scalars := sessionClassificationCollectorScalars(file)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "Collect" || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		receiver := fn.Recv.List[0].Names[0].Name
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sessionClassificationMutexCalls[sel.Sel.Name] {
					return false
				}
			}
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == receiver && scalars[sel.Sel.Name] {
				rendered[sel.Sel.Name] = true
			}
			return true
		})
	}
	return rendered
}

// sessionClassificationTouchedFields returns the collector fields a method reads
// or writes.
func sessionClassificationTouchedFields(fn *ast.FuncDecl, receiver string) map[string]bool {
	fields := map[string]bool{}
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == receiver {
			fields[sel.Sel.Name] = true
		}
		return true
	})
	return fields
}

func sessionClassificationSortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
