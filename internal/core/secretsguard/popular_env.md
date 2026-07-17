# Popular secret environment names

Curated exact names loaded when `single_user.include_popular_env` is true.

## Included (secret material)

- `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`
- `GITHUB_TOKEN`, `GH_TOKEN`, `GITLAB_TOKEN`
- `NPM_TOKEN`, `PYPI_TOKEN`
- `SLACK_BOT_TOKEN`, `STRIPE_SECRET_KEY`
- `AZURE_CLIENT_SECRET`, `GOOGLE_API_KEY`
- plus other common API/token names in `PopularSecretEnvNames`

## Explicitly excluded (unless `include_env`)

These are IDs, profiles, or filesystem paths — not secret values suitable for exact-match scanning:

- `AWS_ACCESS_KEY_ID`
- `AWS_PROFILE`
- `GOOGLE_APPLICATION_CREDENTIALS`
- `CURL_CA_BUNDLE`
