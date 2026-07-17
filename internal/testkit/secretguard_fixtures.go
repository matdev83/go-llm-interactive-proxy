package testkit

// Synthetic secret fixtures for secrets-guard acceptance and contract tests.
// These are not production or customer credentials. Tests must never print these
// values (or meaningful substrings) in failure messages, logs, or error strings.

const (
	// SyntheticOpenAIAPIKey is an OpenAI-shaped placeholder for catalog/matcher tests.
	SyntheticOpenAIAPIKey = "sk-test-openai-secretguard-fixture-001" // #nosec G101 -- synthetic fixture only.

	// SyntheticOpenRouterAPIKey is an OpenRouter-shaped placeholder.
	SyntheticOpenRouterAPIKey = "sk-or-test-secretguard-fixture-002" // #nosec G101 -- synthetic fixture only.

	// SyntheticAnthropicSecretGuardKey is an Anthropic-shaped placeholder distinct from SyntheticAnthropicAPIKey.
	SyntheticAnthropicSecretGuardKey = "sk-ant-test-secretguard-fixture-003" // #nosec G101 -- synthetic fixture only.

	// SyntheticGeminiAPIKey is a Gemini/Google-shaped placeholder.
	SyntheticGeminiAPIKey = "AIzaSyTestSecretGuardFixture0004xxxx" // #nosec G101 -- synthetic fixture only.

	// SyntheticBearerCredential is a multi-user auth credential placeholder (request-scoped matcher only).
	SyntheticBearerCredential = "lip-mu-test-secretguard-bearer-005" // #nosec G101 -- synthetic fixture only.

	// SyntheticShortSecret is intentionally below the default min_secret_bytes (8) for exclusion tests.
	SyntheticShortSecret = "short" // #nosec G101 -- synthetic fixture only.

	// SyntheticOverlapLonger is the longer of two overlapping synthetic values (longest-match wins).
	SyntheticOverlapLonger = "secretguard-overlap-longer-value-aa" // #nosec G101 -- synthetic fixture only.

	// SyntheticOverlapShorter is a proper prefix of SyntheticOverlapLonger.
	SyntheticOverlapShorter = "secretguard-overlap" // #nosec G101 -- synthetic fixture only.

	// SyntheticUnicodeSecret embeds non-ASCII bytes for UTF-8 length preservation tests.
	SyntheticUnicodeSecret = "sg-ünîcode-секрет-006" // #nosec G101 -- synthetic fixture only.

	// SyntheticDuplicateValueAliasA and SyntheticDuplicateValueAliasB share the same value under different names.
	SyntheticDuplicateValueAliasA = "sg-dup-shared-value-fixture-007" // #nosec G101 -- synthetic fixture only.
	SyntheticDuplicateValueAliasB = SyntheticDuplicateValueAliasA
)

// SyntheticSecretGuardEnvNames are safe environment-variable *names* used in catalog tests.
// Values are never asserted by printing; use the Synthetic* constants above.
var SyntheticSecretGuardEnvNames = []string{
	"OPENAI_API_KEY",
	"OPENAI_API_KEY_2",
	"OPENAI_API_KEY_7",
	"OPENROUTER_API_KEY",
	"ANTHROPIC_API_KEY",
	"GEMINI_API_KEY",
	"LIP_TEST_SECRETGUARD_INCLUDE",
}

// AllSyntheticSecretGuardValues returns every synthetic secret string used by secrets-guard tests.
// Callers should scan logs/errors for these values and fail on any occurrence outside the private matcher.
func AllSyntheticSecretGuardValues() []string {
	return []string{
		SyntheticOpenAIAPIKey,
		SyntheticOpenRouterAPIKey,
		SyntheticAnthropicSecretGuardKey,
		SyntheticGeminiAPIKey,
		SyntheticBearerCredential,
		SyntheticShortSecret,
		SyntheticOverlapLonger,
		SyntheticOverlapShorter,
		SyntheticUnicodeSecret,
		SyntheticDuplicateValueAliasA,
	}
}

// AllSyntheticSecretGuardNeedles returns meaningful substrings that should also
// never appear outside the private matcher. These are shorter than the full
// fixture values so regression checks catch partial leaks.
func AllSyntheticSecretGuardNeedles() []string {
	return []string{
		"secretguard-fixture-001",
		"secretguard-fixture-002",
		"secretguard-fixture-003",
		"TestSecretGuardFixture0004",
		"secretguard-bearer-005",
		"secretguard-overlap",
		"sg-ünîcode",
		"dup-shared-value-fixture-007",
	}
}
