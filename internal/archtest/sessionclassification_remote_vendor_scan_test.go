package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// sessionClassificationVendorAdapterPackage is the single package allowed to hold
// vendor wire details. design.md 701-712 and tasks.md 8.2 own the concrete
// TypeSafe/Jev HTTP adapter, and it is the only place a guessed endpoint,
// request/response shape, or auth format may be written down: the remote contract
// itself must stay provider-neutral (requirements 6.1, 11.5; design.md 40, 699,
// 1084; research.md 258).
//
// The exemption is an exact package directory, not a prefix: a nested adapter
// subpackage, a helper package, or a plugin package is still a violation, so the
// vendor surface can widen only through an explicit edit of this list.
var sessionClassificationVendorAdapterPackage = "internal/standardplugins/featurehost/sessionclassification"

// sessionClassificationContractPackages are the provider-neutral contract
// packages. They must never name a remote endpoint at all: the remote port declares
// no URL for an adapter to fill in, because no frozen early-access vendor
// specification exists (design.md 40; research.md 258). They are also the only
// packages where the literal rules run without a vendor marker; see
// sessionClassificationContractWireDetail.
var sessionClassificationContractPackages = []string{
	"internal/plugins/features/sessionclassification",
	"pkg/lipsdk/sessionclassification",
}

// sessionClassificationVendorMarkers name the remote vendor surface. The match is
// a case-insensitive substring test against one import-path segment, against the
// local alias an import is bound to, and against a declared type name. It is
// therefore a substring test on a segment, not an exact-segment or whole-word
// test, and it can only see text that carries a marker at all: the residual
// limitation is stated on sessionClassificationVendorFindings.
var sessionClassificationVendorMarkers = []string{"typesafe", "jev"}

// sessionClassificationCredentialReferenceKey is the one transport-vocabulary word
// the provider-neutral contract is required to name. Requirement 8.3 makes the
// credential a referenced environment name, so the contract owns the key that
// configures it; requirement 7.1 and 7.5 forbid the credential itself. Naming the
// key is therefore vocabulary, while using the word as a value, a header format,
// or a JSON field is a frozen wire detail.
const sessionClassificationCredentialReferenceKey = "api_key_env"

// sessionClassificationWireShapeMarkers are the transport shapes a vendor wire
// detail always carries. A string literal needs a vendor marker and one of these
// before it is treated as a frozen endpoint, request/response shape, or auth
// format; the provider's own mode and provider vocabulary ("jev") stays legal.
// Inside the provider-neutral contract packages one of these is sufficient on its
// own, because requirement 7.1 forbids Authorization headers, credentials, and
// resume tokens on the wire whatever vendor they belong to; see
// sessionClassificationContractWireDetail.
var sessionClassificationWireShapeMarkers = []string{
	"://", "bearer", "authorization", "api_key", "apikey", "secret", "token", "endpoint",
}

// TestSessionClassificationVendorDetailsStayInsideTheAdapterPackage scans the live
// production tree: no TypeSafe/Jev import, no vendor-shaped type declaration, and no
// frozen wire detail may appear outside the one adapter package. This is the
// architecture assertion that vendor DTOs cannot cross the remote boundary
// (requirements 6.1, 7.1, 11.5, 12.11).
func TestSessionClassificationVendorDetailsStayInsideTheAdapterPackage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(sessionClassificationVendorAdapterPackage))); err != nil {
		t.Fatalf("vendor adapter package %s must exist: %v", sessionClassificationVendorAdapterPackage, err)
	}

	var violations []string
	if err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		findings, err := sessionClassificationVendorFindings(PackageDirFromRel(rel), rel, src)
		if err != nil {
			return err
		}
		violations = append(violations, findings...)
		return nil
	}); err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("vendor wire details escaped the remote adapter package (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestSessionClassificationContractsNameNoRemoteEndpoint keeps the contract
// packages free of any endpoint or URL literal. A frozen guessed endpoint in the
// port or the SDK evidence contract is exactly what research.md 258 and design.md
// 40 forbid, and an adapter that has to read one from configuration can always
// receive it from the validated provider configuration instead.
func TestSessionClassificationContractsNameNoRemoteEndpoint(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var violations []string
	if err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		findings, err := sessionClassificationEndpointFindings(PackageDirFromRel(rel), rel, src)
		if err != nil {
			return err
		}
		violations = append(violations, findings...)
		return nil
	}); err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("session-classification contracts name a remote endpoint (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestSessionClassificationVendorBoundaryScannerRejectsHostileSources is the
// load-bearing self-test for the scanner above. Each fixture is a complete,
// otherwise-legitimate production file that smuggles exactly one vendor wire
// detail, so a passing live scan cannot be explained by a scanner that rejects
// nothing or by one that rejects everything.
func TestSessionClassificationVendorBoundaryScannerRejectsHostileSources(t *testing.T) {
	t.Parallel()

	// The fixture table lives in sessionclassification_remote_vendor_fixtures_test.go
	// so this file stays inside the archtest maintainability limit.
	for _, tc := range sessionClassificationVendorBoundaryCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings, err := sessionClassificationVendorFindings(PackageDirFromRel(tc.relPath), tc.relPath, []byte(tc.src))
			if err != nil {
				t.Fatalf("scan %s: %v", tc.relPath, err)
			}
			if len(findings) != tc.wantViolations {
				t.Fatalf("scan %s reported %d violations, want %d: %s",
					tc.relPath, len(findings), tc.wantViolations, strings.Join(findings, "; "))
			}
			if tc.wantViolations > 0 {
				// The finding must name the file so a reviewer can find it, and it
				// must not echo the literal value, which may be credential-shaped.
				if !strings.Contains(findings[0], tc.relPath) {
					t.Fatalf("finding %q does not name %s", findings[0], tc.relPath)
				}
				if strings.Contains(findings[0], "jev-service-token") {
					t.Fatalf("finding echoed a credential-shaped literal: %q", findings[0])
				}
			}
		})
	}
}

// TestSessionClassificationEndpointScannerRejectsFrozenURLs is the self-test for the
// contract endpoint rule above.
func TestSessionClassificationEndpointScannerRejectsFrozenURLs(t *testing.T) {
	t.Parallel()

	const feature = "internal/plugins/features/sessionclassification"
	cases := []struct {
		name           string
		relPath        string
		src            string
		wantViolations int
	}{
		{
			name:           "clean contract file",
			relPath:        feature + "/remote.go",
			src:            "package sessionclassification\n\nconst modeJev = \"jev\"\n\nconst thresholdError = \"positive_threshold must be greater than zero\"\n",
			wantViolations: 0,
		},
		{
			name:           "frozen vendor endpoint",
			relPath:        feature + "/remote.go",
			src:            "package sessionclassification\n\nconst endpoint = \"https://jev.example/v1/decide\"\n",
			wantViolations: 1,
		},
		{
			name:           "frozen unlabelled endpoint",
			relPath:        feature + "/remote.go",
			src:            "package sessionclassification\n\nvar endpoints = map[string]string{\"decide\": \"https://classifier.internal/v1\"}\n",
			wantViolations: 1,
		},
		{
			name:           "unrelated provider endpoint outside the contracts",
			relPath:        "internal/refclient/anthropicmessages/client.go",
			src:            "package anthropicmessages\n\nconst baseURL = \"https://api.anthropic.com/v1\"\n",
			wantViolations: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings, err := sessionClassificationEndpointFindings(PackageDirFromRel(tc.relPath), tc.relPath, []byte(tc.src))
			if err != nil {
				t.Fatalf("scan %s: %v", tc.relPath, err)
			}
			if len(findings) != tc.wantViolations {
				t.Fatalf("scan %s reported %d endpoint violations, want %d: %s",
					tc.relPath, len(findings), tc.wantViolations, strings.Join(findings, "; "))
			}
		})
	}
}

// sessionClassificationVendorFindings reports every vendor wire detail a production
// file outside the adapter package declares.
//
// Rules, in order:
//
//  1. no import whose path carries a vendor-marker segment and no import bound to a
//     vendor-named local alias, so an aliased vendor SDK cannot enter a
//     provider-neutral package under a marker-free path;
//  2. no declared type whose name carries a vendor marker;
//  3. no string literal that carries both a vendor marker and a transport shape;
//  4. inside the provider-neutral contract packages, additionally no string literal
//     that carries a transport shape or is a JSON request/response shape, even
//     without a vendor marker.
//
// Findings name the file and the position but never the literal value, so a
// credential-shaped constant is not echoed into CI output. A finding may name an
// import path or a local alias: both are Go syntax, so neither can carry a
// credential the way a string literal can.
//
// Residual limitation, stated honestly: this scanner is a textual, name-based
// tripwire, not a proof. It cannot see a vendor package whose path, alias, and
// referenced identifiers are all marker-free; a dot or blank import has no local
// name to inspect at all; and no source scan sees a frozen credential value whose
// text carries no shape marker ("sk-live-..."), a request shape assembled at
// runtime by concatenation or fmt rather than written as one literal, or a value
// decoded from configuration. The reflect-based port gate
// (TestSessionClassificationRemotePortSignatureIsFrozen) and the closed-vocabulary
// validators in ValidateRemoteInput and ValidateRemoteDecision remain the
// structural and runtime halves of the same guarantee, so this layer must not be
// read as a complete account of where a wire detail may live.
func sessionClassificationVendorFindings(pkgDir, relPath string, src []byte) ([]string, error) {
	if pkgDir == sessionClassificationVendorAdapterPackage {
		return nil, nil
	}
	fset, file, err := ParseGoSource(relPath, src)
	if err != nil {
		return nil, err
	}
	violations := sessionClassificationImportFindings(relPath, file)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || !sessionClassificationVendorName(typeSpec.Name.Name) {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s: declares vendor wire type %s; only the %s adapter may",
				relPath, typeSpec.Name.Name, sessionClassificationVendorAdapterPackage))
		}
	}
	for _, literal := range sessionClassificationStringLiterals(file) {
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			continue
		}
		if !sessionClassificationVendorWireLiteral(value) &&
			!sessionClassificationContractWireDetail(pkgDir, value) {
			continue
		}
		violations = append(violations, fmt.Sprintf("%s:%d: freezes a vendor wire detail outside the %s adapter",
			relPath, fset.Position(literal.Pos()).Line, sessionClassificationVendorAdapterPackage))
	}
	return violations, nil
}

// sessionClassificationImportFindings reports every import a vendor SDK can enter
// through. Both halves of the import spec are inspected: the path, because a
// marker-free host can still carry the vendor path, and the local name, because an
// aliased import is ordinary Go style and would otherwise smuggle the vendor SDK
// into a provider-neutral package as jevclient or similar. One import reports at
// most one finding.
func sessionClassificationImportFindings(relPath string, file *ast.File) []string {
	var findings []string
	for _, spec := range file.Imports {
		if spec.Path == nil {
			continue
		}
		importPath := strings.Trim(spec.Path.Value, `"`)
		if sessionClassificationVendorImportPath(importPath) {
			findings = append(findings, fmt.Sprintf("%s: imports vendor package %s", relPath, importPath))
			continue
		}
		if spec.Name != nil && sessionClassificationVendorName(spec.Name.Name) {
			findings = append(findings, fmt.Sprintf("%s: imports a package under vendor-named local alias %s",
				relPath, spec.Name.Name))
		}
	}
	return findings
}

// sessionClassificationEndpointFindings reports every URL-shaped literal in the
// provider-neutral contract packages. The remote port must not name an endpoint:
// an adapter receives its endpoint from validated provider configuration, and no
// early-access vendor specification exists to freeze (design.md 40;
// research.md 258).
func sessionClassificationEndpointFindings(pkgDir, relPath string, src []byte) ([]string, error) {
	if !slices.Contains(sessionClassificationContractPackages, pkgDir) {
		return nil, nil
	}
	fset, file, err := ParseGoSource(relPath, src)
	if err != nil {
		return nil, err
	}
	var findings []string
	for _, literal := range sessionClassificationStringLiterals(file) {
		value, err := strconv.Unquote(literal.Value)
		if err != nil || !strings.Contains(value, "://") {
			continue
		}
		findings = append(findings, fmt.Sprintf("%s:%d: provider-neutral contract %s names a remote endpoint",
			relPath, fset.Position(literal.Pos()).Line, pkgDir))
	}
	return findings, nil
}

func sessionClassificationStringLiterals(file *ast.File) []*ast.BasicLit {
	var literals []*ast.BasicLit
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		literals = append(literals, literal)
		return true
	})
	return literals
}

// sessionClassificationVendorImportPath reports whether any slash-separated segment
// of the import path carries a vendor marker. The path is the right half of the
// import rule; sessionClassificationImportFindings also inspects the local alias.
func sessionClassificationVendorImportPath(importPath string) bool {
	return slices.ContainsFunc(strings.Split(strings.ToLower(importPath), "/"), sessionClassificationVendorName)
}

// sessionClassificationVendorName reports whether name carries a vendor marker. The
// match is a case-insensitive substring, so it fires on a path segment, a local
// import alias, or a declared type name that contains a marker anywhere inside it.
func sessionClassificationVendorName(name string) bool {
	lowered := strings.ToLower(name)
	return slices.ContainsFunc(sessionClassificationVendorMarkers, func(marker string) bool {
		return strings.Contains(lowered, marker)
	})
}

// sessionClassificationVendorWireLiteral is the un-widened literal rule: outside the
// provider-neutral contract packages a frozen wire detail still needs both a vendor
// marker and a transport shape, which is what keeps the provider's own mode and
// provider vocabulary legal everywhere else in the tree.
func sessionClassificationVendorWireLiteral(value string) bool {
	return sessionClassificationVendorName(value) && sessionClassificationWireShape(value)
}

// sessionClassificationContractWireDetail is the widened form of the literal rule,
// applied in the provider-neutral contract packages only. Requiring a vendor marker
// there is not sufficient: a frozen auth format or request shape needs no
// obfuscation at all to escape it, because an identifier may simply omit the vendor
// name. Requirement 7.1 forbids Authorization headers, credentials, and resume
// tokens on the wire whichever vendor owns them, and design.md 40 and research.md
// 258 forbid freezing a guessed request or response shape at all, so a literal that
// carries a transport shape or is a JSON object is a violation on its own.
//
// Scoped to the contract packages on purpose: everywhere else in the tree the
// literal rule still requires a vendor marker, because a reference client
// legitimately owns its own provider's endpoint and auth header.
func sessionClassificationContractWireDetail(pkgDir, value string) bool {
	if !slices.Contains(sessionClassificationContractPackages, pkgDir) {
		return false
	}
	return sessionClassificationRequestShape(value) || sessionClassificationWireShape(
		sessionClassificationContractVocabularyOnly(value))
}

// sessionClassificationContractVocabularyOnly strips the credential-reference key
// before the shape test, so the shipped configuration vocabulary requirement 8.3
// obliges the contract to name stays legal while every other use of the same word is
// reported. A format verb means the key is being interpolated into a value rather
// than named, so a formatted string is never stripped.
func sessionClassificationContractVocabularyOnly(value string) string {
	if strings.Contains(value, "%") {
		return value
	}
	return strings.ReplaceAll(value, sessionClassificationCredentialReferenceKey, " ")
}

// sessionClassificationRequestShape reports whether value is a frozen
// request/response shape. A brace-delimited literal whose interior carries a
// double-quoted member name closed immediately by a colon is a JSON object, and a
// JSON object written down in a provider-neutral contract is a guessed wire shape
// no matter what it is called. This is the one shape the textual layer rejects
// structurally rather than by name, which is why it is separate from the marker
// list; the residual limitation on sessionClassificationVendorFindings names the
// shapes it still cannot see.
func sessionClassificationRequestShape(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 2 || !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return false
	}
	return strings.Contains(trimmed, `":`)
}

// sessionClassificationWireShape reports whether value carries any transport-shape
// marker. The match is a case-insensitive substring over the whole literal.
func sessionClassificationWireShape(value string) bool {
	lowered := strings.ToLower(value)
	return slices.ContainsFunc(sessionClassificationWireShapeMarkers, func(marker string) bool {
		return strings.Contains(lowered, marker)
	})
}
