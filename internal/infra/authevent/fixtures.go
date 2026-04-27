package authevent

// AuthLeakFixtureSecrets are synthetic strings that must never appear in operator-visible
// outputs (logs, HTTP bodies, validation errors) in full-path auth regression tests (task 10.3).
var AuthLeakFixtureSecrets = []string{
	"raw-api-key-FIXTURE_aaaaaaaa",
	"Bearer FIXTURE_bbbbbbbb",
	"sso-fixture-token-cccccccc",
	"resume-proof-fixture-dddddddd",
	"oauth-user-access-fixture-eeeeeeee",
}
