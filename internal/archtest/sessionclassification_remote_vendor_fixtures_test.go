package archtest

// sessionClassificationVendorScanCase is one self-test fixture: a complete,
// otherwise-legitimate production file that smuggles exactly one vendor wire detail
// (or, for the control rows, none), with the number of findings the scanner must
// report for it.
type sessionClassificationVendorScanCase struct {
	name           string
	relPath        string
	src            string
	wantViolations int
}

// sessionClassificationVendorBoundaryCases is the fixture table for
// TestSessionClassificationVendorBoundaryScannerRejectsHostileSources. It is held
// here rather than inline so both halves of the vendor boundary gate - the live
// scanner and its load-bearing self-test - stay readable and inside the archtest
// per-file maintainability limit.
//
// Every fixture is a realistic source file. The rows that matter most are the
// escapes an earlier revision of this scanner accepted: an aliased import under a
// marker-free path, a marker-free frozen auth header format, and a marker-free
// frozen JSON request shape. The rows that protect against over-reach are the
// shipped provider-neutral vocabulary the widened rule must keep accepting.
func sessionClassificationVendorBoundaryCases() []sessionClassificationVendorScanCase {
	adapterFile := sessionClassificationVendorAdapterPackage + "/jev_client.go"
	return []sessionClassificationVendorScanCase{
		{
			name:           "clean provider-neutral file",
			relPath:        "internal/core/runtime/session_classifier.go",
			src:            "package runtime\n\nimport \"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi\"\n\nfunc use(op lipapi.Operation) string { return string(op) }\n",
			wantViolations: 0,
		},
		{
			name:           "provider name and mode vocabulary stays legal",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nconst provider = \"jev\"\n\nvar mode = \"heuristic, jev, or hybrid\"\n",
			wantViolations: 0,
		},
		{
			name:           "vendor import outside the adapter",
			relPath:        "internal/core/runtime/session_classifier.go",
			src:            "package runtime\n\nimport \"github.com/typesafe/jev-go\"\n\nvar client = jev.New()\n",
			wantViolations: 1,
		},
		{
			name:           "vendor import inside a nested adapter subpackage",
			relPath:        sessionClassificationVendorAdapterPackage + "/jevadapter/client.go",
			src:            "package jevadapter\n\nimport \"example.com/jevclient\"\n\nvar client = jevclient.New()\n",
			wantViolations: 1,
		},
		{
			name:           "vendor import inside the adapter package",
			relPath:        adapterFile,
			src:            "package sessionclassification\n\nimport \"github.com/typesafe/jev-go\"\n\nvar client = jev.New()\n",
			wantViolations: 0,
		},
		{
			name:           "vendor type declaration outside the adapter",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\ntype JevRequest struct {\n\tScore float64\n}\n",
			wantViolations: 1,
		},
		{
			name:           "vendor type declaration in another feature package",
			relPath:        "internal/plugins/features/codexclientcompat/match.go",
			src:            "package codexclientcompat\n\ntype typeSafeDecision struct {\n\tScore float64\n}\n",
			wantViolations: 1,
		},
		{
			name:           "frozen endpoint outside the adapter",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nconst remoteEndpoint = \"https://api.typesafe.com/v1/decide\"\n",
			wantViolations: 1,
		},
		{
			name:           "frozen auth header outside the adapter",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nconst remoteAuth = \"Bearer jev-service-token\"\n",
			wantViolations: 1,
		},
		{
			name:           "vendor endpoint inside the adapter package",
			relPath:        adapterFile,
			src:            "package sessionclassification\n\nconst remoteEndpoint = \"https://api.typesafe.com/v1/decide\"\n\nvar authHeader = \"Bearer jev-service-token\"\n",
			wantViolations: 0,
		},
		{
			name:           "unrelated provider base url stays legal",
			relPath:        "internal/refclient/openaichat/client.go",
			src:            "package openaichat\n\nconst baseURL = \"https://api.openai.com/v1\"\n",
			wantViolations: 0,
		},
		{
			// An aliased import is ordinary Go style, not obfuscation: without an
			// alias check the vendor SDK enters the contract package under a
			// marker-free path and the whole textual layer is bypassed.
			name:           "aliased vendor import under a marker-free path",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nimport jevclient \"example.com/classifier\"\n\nvar _ = jevclient.New\n",
			wantViolations: 1,
		},
		{
			name:           "dot import of a vendor sdk outside the adapter",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nimport . \"github.com/typesafe/jev-go\"\n\nvar _ = New\n",
			wantViolations: 1,
		},
		{
			name:           "blank import of a vendor sdk outside the adapter",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nimport _ \"github.com/typesafe/jev-go\"\n",
			wantViolations: 1,
		},
		{
			// The control for the two rows above: an alias under a neutral path is
			// the shape most Go code uses, so aliasing must not be rejected itself.
			name:           "benign alias under a neutral path stays legal",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nimport client \"example.com/other\"\n\nvar _ = client.New\n",
			wantViolations: 0,
		},
		{
			// A frozen auth format needs no obfuscation: the identifier simply omits
			// the vendor name, and the shape marker alone is what betrays it.
			name:           "frozen auth header format without a vendor marker",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nconst authHeaderFormat = \"Authorization: Bearer %s\"\n",
			wantViolations: 1,
		},
		{
			name:           "frozen request shape without a vendor marker",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nconst requestShape = `{\"probability\": 0.9}`\n",
			wantViolations: 1,
		},
		{
			// Controls for the widened contract-package rule: shipped threshold,
			// credential-reference, and error vocabulary must not be rejected.
			name:           "shipped threshold validation string stays legal",
			relPath:        "internal/plugins/features/sessionclassification/config.go",
			src:            "package sessionclassification\n\nconst thresholdReason = \"positive_threshold must be greater than zero and at most one\"\n",
			wantViolations: 0,
		},
		{
			name:           "shipped credential-reference vocabulary stays legal",
			relPath:        "internal/plugins/features/sessionclassification/config.go",
			src:            "package sessionclassification\n\nconst apiKeyEnvKey = \"api_key_env\"\n\nconst apiKeyEnvReason = \"api_key_env must name an environment variable\"\n\nconst apiKeyEnvBounded = \"api_key_env must be a bounded environment name\"\n",
			wantViolations: 0,
		},
		{
			name:           "shipped remote error strings stay legal",
			relPath:        "internal/plugins/features/sessionclassification/remote.go",
			src:            "package sessionclassification\n\nconst notConfigured = \"session classification: remote classification is not configured\"\n\nconst familyReason = \"%w: client family is outside the closed family vocabulary\"\n\nconst modeReason = \"%w: mode %q never constructs or calls a remote decider\"\n",
			wantViolations: 0,
		},
		{
			// The SDK evidence contract is named no endpoint and must name no wire
			// shape either, so the widened rule has to reach it as well as the port.
			name:           "frozen auth header format in the sdk evidence contract",
			relPath:        "pkg/lipsdk/sessionclassification/contracts.go",
			src:            "package sessionclassification\n\nconst authHeaderFormat = \"Authorization: Bearer %s\"\n",
			wantViolations: 1,
		},
		{
			// Naming the credential-reference key is vocabulary; interpolating a value
			// into it is the frozen auth format the contract must not hold, so the
			// format verb is what separates the two and it has to stay load-bearing.
			name:           "formatted credential reference is reported",
			relPath:        "internal/plugins/features/sessionclassification/config.go",
			src:            "package sessionclassification\n\nconst credentialFormat = \"api_key_env=%s\"\n",
			wantViolations: 1,
		},
		{
			// Scope control: the widening belongs to the contract packages only, so a
			// reference client may still hold its own provider auth header.
			name:           "auth-shaped literal outside the contract packages stays legal",
			relPath:        "internal/refclient/openaichat/client.go",
			src:            "package openaichat\n\nconst authHeader = \"Bearer sk-live-example\"\n",
			wantViolations: 0,
		},
	}
}
