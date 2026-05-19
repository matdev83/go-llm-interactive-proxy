package extensions

// Metrics stage labels for observability (distinct constant names from legal pipeline ids in failure_policy.go).
const (
	MetricsStageSessionOpen      = "session_open"
	MetricsStageWorkspaceResolve = "workspace_resolve"
	MetricsStageToolCatalog      = "tool_catalog"
	MetricsStageRequestTransform = "request_transform"
	MetricsStagePreRequest       = "pre_request"
)

// StageMetrics receives extension pipeline timing and fail-open skip counts (optional on [Executor]).
type StageMetrics interface {
	// ObserveStage records wall time for one stage invocation. outcome is ok, error, or fail_open.
	ObserveStage(stage, outcome string, seconds float64)
	// IncFailOpenSkip increments when a fail-open participant returns an error and the chain continues.
	IncFailOpenSkip(stage string)
}
