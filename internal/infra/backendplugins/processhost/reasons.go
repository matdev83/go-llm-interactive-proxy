package processhost

// Reason is a bounded stable process-host outcome code (no paths/secrets).
type Reason string

func (r Reason) Error() string { return string(r) }

const (
	ReasonOK                    Reason = "ok"
	ReasonUnsupportedBinding    Reason = "unsupported_binding"
	ReasonUnsupportedChannel    Reason = "unsupported_channel"
	ReasonPeerRejected          Reason = "peer_rejected"
	ReasonStaleGeneration       Reason = "stale_generation"
	ReasonPIDReuse              Reason = "pid_reuse"
	ReasonLaunchFailed          Reason = "launch_failed"
	ReasonHandshakeFailed       Reason = "handshake_failed"
	ReasonConfigureBeforePeer   Reason = "configure_before_peer"
	ReasonEnvBootstrapRejected  Reason = "env_bootstrap_rejected"
	ReasonLoopbackRejected      Reason = "loopback_rejected"
	ReasonCookieAuthRejected    Reason = "cookie_auth_rejected"
	ReasonShuttingDown          Reason = "shutting_down"
	ReasonGenerationInvalidated Reason = "generation_invalidated"
	ReasonProcessModelViolation Reason = "process_model_violation"
	ReasonMissingRuntime        Reason = "missing_runtime"
	ReasonArtifactRequired      Reason = "artifact_required"
	ReasonSubstitution          Reason = "substitution"
)
