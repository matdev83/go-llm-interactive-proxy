package archtest

// ClosureForbiddenImports extends ForbiddenImports with the ownership-closure
// ratchets (Task 11.2, Req 12.1/12.2). It lives in its own table so the core
// rule file stays under the archtest file-size cap; scanners evaluate both
// slices together through allForbiddenImportRules.
var ClosureForbiddenImports = []ForbiddenImportRule{
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
