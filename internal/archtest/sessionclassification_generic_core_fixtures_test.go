package archtest

// Fixture table for TestGenericCoreCodingClientBranchDetectorRejectsHostileSources.
// It lives in its own file so the detector and its live sweep stay inside the
// archtest 500-line maintainability limit, following the same precedent as
// sessionClassificationVendorBoundaryCases.
type genericCoreBranchHostileCase struct {
	name string
	src  string
	want int
}

func genericCoreBranchHostileCases() []genericCoreBranchHostileCase {
	return []genericCoreBranchHostileCase{
		{
			name: "switch over a concrete coding-client family",
			src: "package runtime\n" +
				"func pick(userAgent string) string {\n" +
				"\tswitch userAgent {\n" +
				"\tcase \"codex\":\n" +
				"\t\treturn \"codex\"\n" +
				"\tcase \"cline\":\n" +
				"\t\treturn \"cline\"\n" +
				"\t}\n" +
				"\treturn \"\"\n" +
				"}\n",
			want: 2,
		},
		{
			name: "equality branch on a concrete coding-client family",
			src: "package runtime\n" +
				"func isCodex(userAgent string) bool {\n" +
				"\treturn userAgent == \"codex_cli_rs/1.2.3\"\n" +
				"}\n",
			want: 1,
		},
		{
			name: "identity-matching call on a concrete coding-client family",
			src: "package runtime\n" +
				"import \"strings\"\n" +
				"func looksCoded(userAgent string) bool {\n" +
				"\treturn strings.Contains(userAgent, \"opencode\")\n" +
				"}\n",
			want: 1,
		},
		{
			name: "map keyed by concrete coding-client family",
			src: "package runtime\n" +
				"var handlers = map[string]int{\"droid\": 1, \"hermes\": 2}\n",
			want: 2,
		},
		{
			name: "switch over the remote vendor mode",
			src: "package runtime\n" +
				"func endpoint(mode string) string {\n" +
				"\tswitch mode {\n" +
				"\tcase \"jev\":\n" +
				"\t\treturn \"remote\"\n" +
				"\tcase \"typesafe\":\n" +
				"\t\treturn \"vendor\"\n" +
				"\t}\n" +
				"\treturn \"\"\n" +
				"}\n",
			want: 2,
		},
		{
			name: "import of a concrete coding-client package",
			src: "package runtime\n" +
				"import \"github.com/matdev83/go-llm-interactive-proxy/connectors/codex\"\n" +
				"var _ = codex.Name\n",
			want: 1,
		},
		{
			name: "control: opaque accepted User-Agent is not a branch",
			src: "package runtime\n" +
				"func relay(userAgent string) string {\n" +
				"\treturn userAgent\n" +
				"}\n" +
				"const sampleUserAgent = \"codex_cli_rs/1.2.3\"\n",
			want: 0,
		},
		{
			name: "control: provider name in a non-branch literal is not a branch",
			src: "package runtime\n" +
				"const providerName = \"jev\"\n" +
				"var notes = []string{\"typesafe\"}\n",
			want: 0,
		},
		{
			name: "control: short family marker inside a longer word is not a branch",
			src: "package runtime\n" +
				"func enabled(mode string) bool {\n" +
				"\treturn mode == \"pipeline\"\n" +
				"}\n",
			want: 0,
		},
	}
}
