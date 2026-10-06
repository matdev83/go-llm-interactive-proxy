package archtest

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdkclassification "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
)

// sessionClassificationRemoteReviewedPackages is the set of packages a remote
// contract value may name.
var sessionClassificationRemoteReviewedPackages = map[string]bool{
	"github.com/matdev83/go-llm-interactive-proxy/internal/agentfacts":                             true,
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification": true,
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi":                                      true,
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session":                              true,
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification":                true,
}

// sessionClassificationRemoteContractPackages is the exact package set each
// shipped remote contract value must reference. The comparison is an equality, not
// a subset: a remote DTO field typed from any other package - a vendor wire type, a
// provider SDK type, or a new project-local container - is a violation, and an
// allowance no field uses is a stale allowance that must be deleted
// (requirements 11.5, 12.11; design.md 1084 "no TypeSafe DTO outside adapter").
var sessionClassificationRemoteContractPackages = map[string]map[string]bool{
	"RemoteInput":    sessionClassificationRemoteReviewedPackages,
	"RemoteDecision": {},
}

// syntheticVendorToken stands in for a vendor request/response enum crossing the
// port boundary. It is a named string in this gate's own package so it is a
// foreign package for the remote contract while still being a fixed-shape scalar:
// only the package rule can reject it.
type syntheticVendorToken string

// syntheticVendorDTO stands in for a vendor wire struct crossing the port
// boundary. It is rejected by the shape rule rather than the package rule, which
// is why both fixtures exist.
type syntheticVendorDTO struct {
	Endpoint string
	Score    float64
}

// The four fixtures below stand in for the port interface drifting under 8.2/8.3.
// Each is a complete interface, so the signature helper can be pointed at it exactly
// as it is pointed at the shipped RemoteDecider.
type (
	// syntheticNeutralPort is the shipped provider-neutral shape and must stay
	// acceptable.
	syntheticNeutralPort interface {
		Decide(ctx context.Context, in sessionclassification.RemoteInput) (sessionclassification.RemoteDecision, error)
	}
	// syntheticVendorNamedPort adds the vendor-named method the former
	// forbidden-name list claimed to reject.
	syntheticVendorNamedPort interface {
		Decide(ctx context.Context, in sessionclassification.RemoteInput) (sessionclassification.RemoteDecision, error)
		JevRequest(ctx context.Context, in sessionclassification.RemoteInput) (sessionclassification.RemoteDecision, error)
	}
	// syntheticRenamedPort renames the single port method.
	syntheticRenamedPort interface {
		Classify(ctx context.Context, in sessionclassification.RemoteInput) (sessionclassification.RemoteDecision, error)
	}
	// syntheticRawHeaderPort widens the single port method with a raw header bag,
	// which requirement 7.1 forbids on the wire.
	syntheticRawHeaderPort interface {
		Decide(ctx context.Context, in sessionclassification.RemoteInput, headers map[string]string) (sessionclassification.RemoteDecision, error)
	}
)

// TestSessionClassificationRemotePortGuardRejectsWidenedSignatures proves the port
// gate rejects every way the interface can stop being the frozen provider-neutral
// Decide contract. These fixtures stand in for the port drifting under 8.2/8.3: a
// vendor-named or extra method, a renamed method, and a raw header bag widened into
// the single method. Each is checked against the same helper the live gate uses, so
// the gate's teeth are demonstrated rather than assumed.
func TestSessionClassificationRemotePortGuardRejectsWidenedSignatures(t *testing.T) {
	t.Parallel()

	t.Run("provider-neutral ports stay accepted", func(t *testing.T) {
		t.Parallel()

		// Both the shipped port and the synthetic fixture family must stay acceptable,
		// so a rejection below is caused by the widened shape and not by the helper
		// rejecting every interface it is given.
		for name, port := range map[string]reflect.Type{
			"RemoteDecider":        reflect.TypeFor[sessionclassification.RemoteDecider](),
			"syntheticNeutralPort": reflect.TypeFor[syntheticNeutralPort](),
		} {
			if findings := sessionClassificationRemotePortSignatureFindings(port); len(findings) != 0 {
				t.Fatalf("%s is not the frozen provider-neutral port: %s", name, strings.Join(findings, "; "))
			}
		}
	})

	cases := []struct {
		name     string
		port     reflect.Type
		wantText string
	}{
		{
			name: "vendor-named extra method is reported",
			port: reflect.TypeFor[syntheticVendorNamedPort](),
			// The rejected method is named in the finding, which is the diagnostic
			// the former forbidden-name list provided.
			wantText: "JevRequest",
		},
		{
			name:     "renamed single method is reported",
			port:     reflect.TypeFor[syntheticRenamedPort](),
			wantText: "want a Decide method",
		},
		{
			name:     "raw header bag widened into the port method is reported",
			port:     reflect.TypeFor[syntheticRawHeaderPort](),
			wantText: "Decide method has type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := sessionClassificationRemotePortSignatureFindings(tc.port)
			if len(findings) == 0 {
				t.Fatalf("the port guard accepted %s", tc.port)
			}
			if !slices.ContainsFunc(findings, func(finding string) bool {
				return strings.Contains(finding, tc.wantText)
			}) {
				t.Fatalf("findings must report %q; got: %s", tc.wantText, strings.Join(findings, "; "))
			}
		})
	}
}

// sessionClassificationRemotePortSignatureFindings reports every way the port stops
// being the frozen provider-neutral Decide contract.
//
// The method set is pinned by equality, not by a forbidden-name list: the port must
// be an interface, must declare exactly one method, that method must be named Decide,
// and its type must be the frozen provider-neutral signature. A vendor-named,
// transport-named, or credential-named method is rejected by the count or by the
// name lookup rather than by a name blacklist, so there is no name the port can
// carry that this gate would accept.
func sessionClassificationRemotePortSignatureFindings(port reflect.Type) []string {
	if port.Kind() != reflect.Interface {
		return []string{fmt.Sprintf("the remote port is %s, want an interface", port.Kind())}
	}
	want := reflect.TypeFor[func(context.Context, sessionclassification.RemoteInput) (sessionclassification.RemoteDecision, error)]()
	if port.NumMethod() != 1 {
		names := make([]string, 0, port.NumMethod())
		for i := range port.NumMethod() {
			names = append(names, port.Method(i).Name)
		}
		return []string{fmt.Sprintf(
			"the remote port declares %d methods %v, want exactly the Decide port", port.NumMethod(), names)}
	}
	method, ok := port.MethodByName("Decide")
	if !ok {
		return []string{fmt.Sprintf("the remote port declares %v, want a Decide method", port.Method(0).Name)}
	}
	if method.Type != want {
		return []string{fmt.Sprintf(
			"the remote port Decide method has type %s, want the frozen provider-neutral %s", method.Type, want)}
	}
	return nil
}

// TestSessionClassificationRemotePortSignatureIsFrozen pins the provider-neutral
// port method. Any signature change - a vendor request/response type, a raw
// header bag, a credential argument, a second method - breaks this gate instead of
// quietly widening the boundary (requirements 6.1, 11.5).
func TestSessionClassificationRemotePortSignatureIsFrozen(t *testing.T) {
	t.Parallel()

	if findings := sessionClassificationRemotePortSignatureFindings(
		reflect.TypeFor[sessionclassification.RemoteDecider](),
	); len(findings) != 0 {
		t.Fatalf("RemoteDecider is not the frozen provider-neutral port: %s", strings.Join(findings, "; "))
	}
}

// TestSessionClassificationRemoteDTOsReferenceOnlyReviewedPackages walks both
// remote contract values and requires every field to be a fixed-shape scalar from
// the reviewed package set. This is the assertion that a vendor DTO cannot cross
// the boundary: a field typed from a vendor package, a provider SDK, or a
// container cannot be added to RemoteInput or RemoteDecision (requirements 7.2,
// 11.5).
func TestSessionClassificationRemoteDTOsReferenceOnlyReviewedPackages(t *testing.T) {
	t.Parallel()

	for contract, want := range sessionClassificationRemoteContractPackages {
		typeOf := map[string]reflect.Type{
			"RemoteInput":    reflect.TypeFor[sessionclassification.RemoteInput](),
			"RemoteDecision": reflect.TypeFor[sessionclassification.RemoteDecision](),
		}[contract]
		if typeOf == nil {
			t.Fatalf("no shipped remote contract named %s; update this gate", contract)
		}
		t.Run(contract, func(t *testing.T) {
			t.Parallel()

			discovered := map[string]bool{}
			findings := sessionClassificationRemotePortFindings(
				typeOf, contract, sessionClassificationRemoteReviewedPackages, discovered, true,
			)
			if len(findings) != 0 {
				t.Fatalf("%s may not cross the remote boundary: %s", contract, strings.Join(findings, "; "))
			}
			if unexpected := sessionClassificationUnexpectedRemotePortPackages(discovered, want); len(unexpected) != 0 {
				t.Fatalf("%s package closure drifted: %s", contract, strings.Join(unexpected, "; "))
			}
		})
	}
}

// sessionClassificationUnexpectedRemotePortPackages reports a drifted package
// closure in both directions: a package the contract references but does not
// declare, and a package the contract still declares although no field uses it.
// The second direction is what keeps the reviewed set from silently widening.
func sessionClassificationUnexpectedRemotePortPackages(discovered, declared map[string]bool) []string {
	var unexpected []string
	for _, name := range mapsSorted(discovered) {
		if !declared[name] {
			unexpected = append(unexpected, "undeclared package "+name)
		}
	}
	for _, name := range mapsSorted(declared) {
		if !discovered[name] {
			unexpected = append(unexpected, "stale package allowance "+name)
		}
	}
	return unexpected
}

// TestSessionClassificationRemotePortPackageClosureIsAnEquality proves the closure
// rule fires in both directions: a foreign package and a stale allowance are both
// reported, so the reviewed set can neither widen nor drift unnoticed.
func TestSessionClassificationRemotePortClosureIsAnEquality(t *testing.T) {
	t.Parallel()

	reviewed := map[string]bool{
		"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi": true,
	}
	contract := reflect.StructOf([]reflect.StructField{
		{Name: "Operation", Type: reflect.TypeFor[lipapi.Operation]()},
	})

	t.Run("exact closure stays accepted", func(t *testing.T) {
		t.Parallel()

		discovered := map[string]bool{}
		findings := sessionClassificationRemotePortFindings(contract, "RemoteInput", reviewed, discovered, true)
		if len(findings) != 0 || len(sessionClassificationUnexpectedRemotePortPackages(discovered, reviewed)) != 0 {
			t.Fatalf("exact closure reported findings %v and drift %v", findings, discovered)
		}
	})

	t.Run("foreign package is reported", func(t *testing.T) {
		t.Parallel()

		discovered := map[string]bool{}
		findings := sessionClassificationRemotePortFindings(contract, "RemoteInput", map[string]bool{}, discovered, true)
		if len(findings) == 0 {
			t.Fatal("an unreviewed package reference must be reported")
		}
		unexpected := sessionClassificationUnexpectedRemotePortPackages(discovered, map[string]bool{})
		if len(unexpected) != 1 || !strings.Contains(unexpected[0], "undeclared package") {
			t.Fatalf("foreign package reported %v, want exactly one undeclared-package finding", unexpected)
		}
	})

	t.Run("stale allowance is reported", func(t *testing.T) {
		t.Parallel()

		discovered := map[string]bool{}
		sessionClassificationRemotePortFindings(contract, "RemoteInput", reviewed, discovered, true)
		stale := map[string]bool{
			"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi":         true,
			"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session": true,
		}
		unexpected := sessionClassificationUnexpectedRemotePortPackages(discovered, stale)
		if len(unexpected) != 1 || !strings.Contains(unexpected[0], "stale package allowance") {
			t.Fatalf("stale allowance reported %v, want exactly one stale-package finding", unexpected)
		}
	})
}

// TestSessionClassificationRemotePortGuardRejectsForeignTypes is the load-bearing
// self-test for the closure rule above: a fixed-shape scalar from an unreviewed
// package and a foreign struct must both be reported, while a contract of only
// reviewed scalars must stay acceptable.
func TestSessionClassificationRemotePortGuardRejectsForeignTypes(t *testing.T) {
	t.Parallel()

	reviewed := map[string]bool{
		"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi":                       true,
		"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification": true,
	}
	cases := []struct {
		name  string
		field reflect.StructField
	}{
		{
			name:  "vendor enum typed field",
			field: reflect.StructField{Name: "VendorCode", Type: reflect.TypeFor[syntheticVendorToken]()},
		},
		{
			name:  "vendor struct typed field",
			field: reflect.StructField{Name: "Extra", Type: reflect.TypeFor[syntheticVendorDTO]()},
		},
		{
			name:  "foreign enum under a benign name",
			field: reflect.StructField{Name: "Family", Type: reflect.TypeFor[syntheticVendorToken]()},
		},
		{
			name:  "credential-shaped foreign enum",
			field: reflect.StructField{Name: "APIKey", Type: reflect.TypeFor[syntheticVendorToken]()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := reflect.StructOf([]reflect.StructField{
				{Name: "Operation", Type: reflect.TypeFor[lipapi.Operation]()},
				tc.field,
			})
			findings := sessionClassificationRemotePortFindings(fixture, "RemoteInput", reviewed, map[string]bool{}, true)
			if len(findings) == 0 {
				t.Fatalf("the port guard accepted a field typed %s", tc.field.Type)
			}
			if !slices.ContainsFunc(findings, func(finding string) bool {
				return strings.Contains(finding, "."+tc.field.Name)
			}) {
				t.Fatalf("findings must name the offending field %s; got: %s", tc.field.Name, strings.Join(findings, "; "))
			}
		})
	}

	t.Run("reviewed scalar contract stays accepted", func(t *testing.T) {
		t.Parallel()

		fixture := reflect.StructOf([]reflect.StructField{
			{Name: "Operation", Type: reflect.TypeFor[lipapi.Operation]()},
			{Name: "ToolCategories", Type: reflect.TypeFor[sdkclassification.ToolCategorySet]()},
			{Name: "LocalEvidenceCode", Type: reflect.TypeFor[session.EvidenceCode]()},
			{Name: "ClientFamily", Type: reflect.TypeFor[syntheticVendorToken]()},
			{Name: "HasAmbiguousClient", Type: reflect.TypeFor[bool]()},
			{Name: "CodingProbability", Type: reflect.TypeFor[float64]()},
		})
		findings := sessionClassificationRemotePortFindings(fixture, "RemoteInput", map[string]bool{
			"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi":                       true,
			"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification": true,
			"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session":               true,
			reflect.TypeFor[syntheticVendorToken]().PkgPath():                               true,
		}, map[string]bool{}, true)
		if len(findings) != 0 {
			t.Fatalf("a contract of reviewed fixed-shape scalars must stay acceptable: %s", strings.Join(findings, "; "))
		}
	})

	t.Run("container and dynamic shapes are rejected", func(t *testing.T) {
		t.Parallel()

		for _, field := range []reflect.StructField{
			{Name: "Messages", Type: reflect.TypeFor[[]string]()},
			{Name: "RawHeaders", Type: reflect.TypeFor[map[string][]string]()},
			{Name: "Deferred", Type: reflect.TypeFor[*string]()},
			{Name: "Carrier", Type: reflect.TypeFor[any]()},
			{Name: "Compile", Type: reflect.TypeFor[func()]()},
			{Name: "History", Type: reflect.TypeFor[[8]string]()},
			{Name: "Handle", Type: reflect.TypeFor[uintptr]()},
		} {
			fixture := reflect.StructOf([]reflect.StructField{
				{Name: "Operation", Type: reflect.TypeFor[lipapi.Operation]()},
				field,
			})
			findings := sessionClassificationRemotePortFindings(fixture, "RemoteInput", reviewed, map[string]bool{}, true)
			if len(findings) == 0 {
				t.Fatalf("the port guard accepted an unbounded field %s %s", field.Name, field.Type)
			}
		}
	})
}

// sessionClassificationRemotePortFindings reports every way a remote contract
// value could reference unreviewed code or own unbounded storage.
//
// Rules, in order:
//
//  1. every field is a fixed-shape scalar (bool, string, or a fixed-width
//     numeric). A struct, container, pointer, interface, func, chan, or raw
//     handle is a violation, because the remote contract is flat by construction;
//  2. every named field type is declared in one of the reviewed packages, which is
//     recorded in packages when collect is set.
//
// Residual limitation, stated honestly: this guard reads type identity and storage
// shape only. It cannot inspect what a fixed-shape string carries at runtime, so
// the closed-vocabulary checks in ValidateRemoteInput and ValidateRemoteDecision
// remain the runtime half of the same guarantee.
func sessionClassificationRemotePortFindings(
	typ reflect.Type,
	path string,
	reviewed map[string]bool,
	packages map[string]bool,
	collect bool,
) []string {
	if typ.Kind() != reflect.Struct {
		return nil
	}
	var findings []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		fieldPath := path + "." + field.Name
		fieldType := field.Type
		switch fieldType.Kind() {
		case reflect.Bool, reflect.String,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
		default:
			findings = append(findings, fmt.Sprintf(
				"%s has unbounded, dynamic, or non-scalar shape (%s); the remote contract owns no container",
				fieldPath, fieldType.Kind()))
			continue
		}
		if fieldType.PkgPath() == "" {
			continue
		}
		if collect && packages != nil {
			packages[fieldType.PkgPath()] = true
		}
		if !reviewed[fieldType.PkgPath()] {
			findings = append(findings, fmt.Sprintf(
				"%s is typed %s from unreviewed package %s; a vendor DTO or provider SDK type cannot cross the remote boundary",
				fieldPath, fieldType, fieldType.PkgPath()))
		}
	}
	return findings
}

func mapsSorted(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
