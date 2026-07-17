package secretsguard

// PopularSecretEnvNames is the curated exact-name registry of common non-proxy
// credential environment variables loaded when SingleUserOptions.IncludePopularEnv
// is true. Credential *paths*, *ids*, and profile names are intentionally omitted.
//
// Explicitly not included (unless listed in IncludeEnv):
//   - AWS_ACCESS_KEY_ID
//   - AWS_PROFILE
//   - GOOGLE_APPLICATION_CREDENTIALS
//   - CURL_CA_BUNDLE
var PopularSecretEnvNames = []string{
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"GITHUB_TOKEN",
	"GH_TOKEN",
	"GITLAB_TOKEN",
	"NPM_TOKEN",
	"PYPI_TOKEN",
	"SLACK_BOT_TOKEN",
	"STRIPE_SECRET_KEY",
	"AZURE_CLIENT_SECRET",
	"GOOGLE_API_KEY",
	"DIGITALOCEAN_ACCESS_TOKEN",
	"TWILIO_AUTH_TOKEN",
	"SENDGRID_API_KEY",
	"HEROKU_API_KEY",
	"CLOUDFLARE_API_TOKEN",
	"DATADOG_API_KEY",
	"SENTRY_AUTH_TOKEN",
	"TERRAFORM_TOKEN",
	"VAULT_TOKEN",
}
