package trust

// Reason is a bounded stable trust failure/success code (no paths/secrets).
type Reason string

func (r Reason) Error() string { return string(r) }

const (
	ReasonOK                  Reason = "ok"
	ReasonPathEscape          Reason = "path_escape"
	ReasonNotRegular          Reason = "not_regular_file"
	ReasonNotExecutableType   Reason = "not_native_executable"
	ReasonUnsupportedPlatform Reason = "unsupported_platform"
	ReasonDigestMismatch      Reason = "digest_mismatch"
	ReasonSubstitution        Reason = "substitution_detected"
	ReasonStagingUnsupported  Reason = "staging_unsupported"
	ReasonStagingFailed       Reason = "staging_failed"
	ReasonRootRequired        Reason = "trusted_root_required"
	ReasonSymlinkEscape       Reason = "symlink_escape"
	ReasonOpenFailed          Reason = "open_failed"
	ReasonACLUnverified       Reason = "acl_unverified"
)
