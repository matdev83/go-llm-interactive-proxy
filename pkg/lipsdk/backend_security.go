package lipsdk

// BackendCredentialMode describes how a registered backend plugin obtains upstream credentials
// (startup metadata only; not plugin-private configuration values).
type BackendCredentialMode string

const (
	// CredentialStatic uses operator-configured static credentials such as API keys.
	CredentialStatic BackendCredentialMode = "static"
	// CredentialWorkload uses workload identity from the local runtime environment.
	CredentialWorkload BackendCredentialMode = "workload"
	// CredentialOAuthUser uses user-scoped OAuth credentials; eligibility is validated against access mode.
	CredentialOAuthUser BackendCredentialMode = "oauth_user"
	// CredentialUnknown means the factory did not declare a credential posture; validation may treat this conservatively.
	CredentialUnknown BackendCredentialMode = "unknown"
)

// BackendSecurityProfile is stable startup metadata for backend credential posture. It is
// part of the public plugin registration contract and must not hold secret values.
type BackendSecurityProfile struct {
	CredentialMode BackendCredentialMode
}
