package archtest

// ClosureForbiddenImports extends ForbiddenImports with the ownership-closure
// ratchets (Task 11.2, Req 12.1/12.2). It lives in its own table so the core
// rule file stays under the archtest file-size cap; scanners evaluate both
// slices together through allForbiddenImportRules.
var ClosureForbiddenImports = []ForbiddenImportRule{
	// Public contract and SDK packages stay on public contracts: no internal
	// packages, no SQL/ORM, no provider SDKs, and no optional connector modules.
	{SourcePattern: "pkg/lipapi", TargetPattern: "/internal/", Reason: "canonical contracts must not import internal packages"},
	{SourcePattern: "pkg/lipapi", TargetPattern: "/connectors/", Reason: "canonical contracts must not import optional connector modules"},
	{SourcePattern: "pkg/lipapi", TargetPattern: "/connector-support/", Reason: "canonical contracts must not import optional connector support"},
	{SourcePattern: "pkg/lipapi", TargetPattern: "github.com/openai/", Reason: "canonical contracts must not import provider SDKs"},
	{SourcePattern: "pkg/lipapi", TargetPattern: "github.com/anthropics/", Reason: "canonical contracts must not import provider SDKs"},
	{SourcePattern: "pkg/lipapi", TargetPattern: "github.com/aws/", Reason: "canonical contracts must not import provider SDKs"},
	{SourcePattern: "pkg/lipapi", TargetPattern: "google.golang.org/genai", Reason: "canonical contracts must not import provider SDKs"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "/internal/", Reason: "public plugin SDK must not import internal packages"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "/connectors/", Reason: "public plugin SDK must not import optional connector modules"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "/connector-support/", Reason: "public plugin SDK must not import optional connector support"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "github.com/openai/", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "github.com/anthropics/", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "github.com/aws/", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "google.golang.org/genai", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "github.com/google/generative-ai-go/", Reason: "public plugin SDK must not import provider SDKs"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "database/sql", Reason: "public plugin SDK contracts must not depend on SQL"},
	{SourcePattern: "pkg/lipsdk", TargetPattern: "github.com/uptrace/bun", Reason: "public plugin SDK contracts must not depend on an ORM"},
	{
		SourcePattern: "pkg/lipruntime",
		TargetPattern: "/internal/plugins/features/",
		Reason:        "public facade must not depend on concrete feature plugins; use SDK registrations",
	},
	{
		SourcePattern: "internal/plugins/features",
		TargetPattern: "/internal/core",
		Reason:        "feature tree must not depend on internal/core (use pkg/lipsdk contracts)",
	},
	{
		SourcePattern: "internal/plugins/features",
		TargetPattern: "/internal/infra/runtimebundle",
		Reason:        "feature tree must not depend on runtimebundle",
	},
	{
		SourcePattern: "internal/plugins/features",
		TargetPattern: "/internal/standardplugins/featurehost",
		Reason:        "feature tree must not depend on standard featurehost composition",
	},
}

func allForbiddenImportRules() []ForbiddenImportRule {
	out := make([]ForbiddenImportRule, 0, len(ForbiddenImports)+len(ClosureForbiddenImports))
	out = append(out, ForbiddenImports...)
	out = append(out, ClosureForbiddenImports...)
	return out
}
