package archtest

// KnownPlaneFields maps struct field and identifier names to plane metadata.
var KnownPlaneFields = map[string]PlaneFieldMetadata{
	// Wave 1: Hooks
	"SubmitHooks":            {PlaneID: "submit_hooks", Wave: Wave1_HookBus},
	"RequestPartHooks":       {PlaneID: "request_part_hooks", Wave: Wave1_HookBus},
	"ResponsePartHooks":      {PlaneID: "response_part_hooks", Wave: Wave1_HookBus},
	"ToolReactors":           {PlaneID: "tool_reactors", Wave: Wave1_HookBus},
	"ToolReactorErrorPolicy": {PlaneID: "tool_reactor_error_policy", Wave: Wave1_HookBus},

	// Wave 2: Observers
	"TrafficObservers":        {PlaneID: "traffic_observers", Wave: Wave2_Observers},
	"UsageObservers":          {PlaneID: "usage_observers", Wave: Wave2_Observers},
	"RawCaptureSinks":         {PlaneID: "raw_capture_sinks", Wave: Wave2_Observers},
	"TrafficRedactors":        {PlaneID: "traffic_redactors", Wave: Wave2_Observers},
	"StreamObserverFactories": {PlaneID: "stream_observer_factories", Wave: Wave2_Observers},

	// Wave 3: Request shaping
	"RequestTransforms":  {PlaneID: "request_transforms", Wave: Wave3_RequestShaping},
	"PreRequestHandlers": {PlaneID: "pre_request_handlers", Wave: Wave3_RequestShaping},
	"RouteHintProviders": {PlaneID: "route_hint_providers", Wave: Wave3_RequestShaping},
	"CompletionGates":    {PlaneID: "completion_gates", Wave: Wave3_RequestShaping},
	"AttemptTransforms":  {PlaneID: "attempt_transforms", Wave: Wave3_RequestShaping},
	"SessionOpeners":     {PlaneID: "session_openers", Wave: Wave3_RequestShaping},
	"WorkspaceResolvers": {PlaneID: "workspace_resolvers", Wave: Wave3_RequestShaping},

	// Wave 4: Tools
	"ToolCatalogFilters":               {PlaneID: "tool_catalog_filters", Wave: Wave4_Tools},
	"ToolCallPolicies":                 {PlaneID: "tool_call_policies", Wave: Wave4_Tools},
	"ToolCallFinalizers":               {PlaneID: "tool_call_finalizers", Wave: Wave4_Tools},
	"ToolCallFinalizationMaxArgsBytes": {PlaneID: "tool_call_finalization_max_args_bytes", Wave: Wave4_Tools},

	// Wave 5a: Secret guard & Compaction
	"SecretGuards":         {PlaneID: "secret_guards", Wave: Wave5a_GuardsCompaction},
	"CompactionObservers":  {PlaneID: "compaction_observers", Wave: Wave5a_GuardsCompaction},
	"CompactionPreservers": {PlaneID: "compaction_preservers", Wave: Wave5a_GuardsCompaction},

	// Wave 5b: Local turn & Terminal decision
	"LocalTurnHandlers":          {PlaneID: "local_turn_handlers", Wave: Wave5b_LocalTurnTerminal},
	"TerminalDecisionProvider":   {PlaneID: "terminal_decision_provider", Wave: Wave5b_LocalTurnTerminal},
	"terminalDecisionProviderID": {PlaneID: "terminal_decision_provider", Wave: Wave5b_LocalTurnTerminal},
	"terminalDecisionProvider":   {PlaneID: "terminal_decision_provider", Wave: Wave5b_LocalTurnTerminal},
}

// KnownPlaneIDs maps canonical plane IDs to plane metadata.
var KnownPlaneIDs = map[string]PlaneFieldMetadata{
	"submit_hooks":                          {PlaneID: "submit_hooks", Wave: Wave1_HookBus},
	"request_part_hooks":                    {PlaneID: "request_part_hooks", Wave: Wave1_HookBus},
	"response_part_hooks":                   {PlaneID: "response_part_hooks", Wave: Wave1_HookBus},
	"tool_reactors":                         {PlaneID: "tool_reactors", Wave: Wave1_HookBus},
	"tool_reactor_error_policy":             {PlaneID: "tool_reactor_error_policy", Wave: Wave1_HookBus},
	"traffic_observers":                     {PlaneID: "traffic_observers", Wave: Wave2_Observers},
	"usage_observers":                       {PlaneID: "usage_observers", Wave: Wave2_Observers},
	"raw_capture_sinks":                     {PlaneID: "raw_capture_sinks", Wave: Wave2_Observers},
	"traffic_redactors":                     {PlaneID: "traffic_redactors", Wave: Wave2_Observers},
	"stream_observer_factories":             {PlaneID: "stream_observer_factories", Wave: Wave2_Observers},
	"request_transforms":                    {PlaneID: "request_transforms", Wave: Wave3_RequestShaping},
	"pre_request_handlers":                  {PlaneID: "pre_request_handlers", Wave: Wave3_RequestShaping},
	"route_hint_providers":                  {PlaneID: "route_hint_providers", Wave: Wave3_RequestShaping},
	"completion_gates":                      {PlaneID: "completion_gates", Wave: Wave3_RequestShaping},
	"attempt_transforms":                    {PlaneID: "attempt_transforms", Wave: Wave3_RequestShaping},
	"session_openers":                       {PlaneID: "session_openers", Wave: Wave3_RequestShaping},
	"workspace_resolvers":                   {PlaneID: "workspace_resolvers", Wave: Wave3_RequestShaping},
	"tool_catalog_filters":                  {PlaneID: "tool_catalog_filters", Wave: Wave4_Tools},
	"tool_call_policies":                    {PlaneID: "tool_call_policies", Wave: Wave4_Tools},
	"tool_call_finalizers":                  {PlaneID: "tool_call_finalizers", Wave: Wave4_Tools},
	"tool_call_finalization_max_args_bytes": {PlaneID: "tool_call_finalization_max_args_bytes", Wave: Wave4_Tools},
	"secret_guards":                         {PlaneID: "secret_guards", Wave: Wave5a_GuardsCompaction},
	"compaction_observers":                  {PlaneID: "compaction_observers", Wave: Wave5a_GuardsCompaction},
	"compaction_preservers":                 {PlaneID: "compaction_preservers", Wave: Wave5a_GuardsCompaction},
	"local_turn_handlers":                   {PlaneID: "local_turn_handlers", Wave: Wave5b_LocalTurnTerminal},
	"terminal_decision_provider":            {PlaneID: "terminal_decision_provider", Wave: Wave5b_LocalTurnTerminal},
}

// Whitelisted non-plane fields for individual structs.
var (
	AllowedFeatureBundleFields = map[string]bool{
		"SchemaVersion": true,
		"PlaneSet":      true,
		"Lifecycles":    true,
	}

	AllowedMergedSurfaceFields = map[string]bool{
		"Lifecycles": true,
		"frozen":     true,
		"Frozen":     true,
	}

	AllowedExtensionsOptionsFields = map[string]bool{
		"SecretGuardInputs":      true,
		"SecretGuardEnvironment": true,
		"SecretDecisionObserver": true,
		"frozen":                 true,
		"Frozen":                 true,
		"PlaneSet":               true,
	}

	AllowedGenerationOperationsFields = map[string]bool{
		"terminalProviders":       true,
		"readiness":               true,
		"frozen":                  true,
		"Frozen":                  true,
		"keepwarmAccounting":      true,
		"billingReports":          true,
		"billingReportsPath":      true,
		"billingProvisioner":      true,
		"billingExposureRecovery": true,
	}

	AllowedRequestRuntimeSnapshotFields = map[string]bool{
		"hookBus":          true,
		"state":            true,
		"aux":              true,
		"obs":              true,
		"usageObs":         true,
		"raw":              true,
		"ws":               true,
		"secretGuardPlane": true,
		"policyObserver":   true,
		"timeoutBudget":    true,
		"timeoutGuard":     true,
		"featurePlanes":    true,
		"gen":              true,
	}

	// AllowedStageConsumers is the explicit allowlist of fully-qualified Go symbol paths
	// permitted to act as stage consumers accessing extension planes. Any stage-consumer accessor
	// or method outside this allowlist is rejected by the forbidden-mirror architecture scanner.
	AllowedStageConsumers = map[string]bool{
		"internal/core/extensions.(*RequestRuntimeSnapshot).CompletionGates":                  true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).RequestTransforms":                true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).PreRequestHandlers":               true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).RouteHintProviders":               true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).AttemptTransforms":                true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).SessionOpeners":                   true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).Workspace":                        true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCatalogFilters":               true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallPolicies":                 true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallPoliciesExecution":        true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallFinalizers":               true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallFinalizersExecution":      true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).SecretGuards":                     true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).SecretGuardsExecution":            true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).CompactionObservers":              true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).CompactionPreservers":             true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).LocalTurnHandlers":                true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).LocalTurnHandlersExecution":       true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TerminalDecisionProvider":         true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TerminalDecisionProviderIdentity": true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TrafficObserver":                  true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).UsageObserver":                    true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).RawCapture":                       true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TrafficRedactors":                 true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).StreamObserverFactories":          true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TrafficPortBundle":                true,
		"internal/core/extensions.CompletionGatesFromContext":                                 true,
	}

	// AllowedHookProjections is the qualified-symbol allowlist of hook-bus projection functions.
	// Any other function whose body reads hook-family fields on MergedFeatureSurface past Wave 1 is forbidden.
	AllowedHookProjections = map[string]bool{
		"internal/infra/runtimebundle.HooksConfigFromGenerated": true,
		"internal/infra/runtimebundle.HooksConfigFromFrozen":    true,
	}

	// AllowedObserverProjections is the qualified-symbol allowlist of observer and tool projection functions.
	// Any other function whose body reads observer/tool planes via Get past Wave 2/4 is forbidden.
	AllowedObserverProjections = map[string]bool{
		"internal/infra/runtimebundle.buildRuntimeSnapshot":                 true,
		"internal/infra/runtimebundle.buildExecutorRuntime":                 true,
		"internal/infra/runtimebundle.buildSecretGuardRuntime":              true,
		"internal/infra/compactioncompose.BindFeatureSurface":               true,
		"internal/infra/runtimebundle.bindReasoningPreservationCompression": true,
	}
)

// IsAllowedHookProjection reports whether qualifiedSymbol is in the qualified-symbol allowlist for hook projection.
func IsAllowedHookProjection(qualifiedSymbol string) bool {
	return AllowedHookProjections[qualifiedSymbol]
}

// IsAllowedObserverProjection reports whether qualifiedSymbol is in the qualified-symbol allowlist for observer projection.
func IsAllowedObserverProjection(qualifiedSymbol string) bool {
	return AllowedObserverProjections[qualifiedSymbol]
}

// IsAllowedStageConsumer checks if a fully-qualified Go symbol is explicitly recorded in AllowedStageConsumers.
func IsAllowedStageConsumer(qualifiedSymbol string) bool {
	return AllowedStageConsumers[qualifiedSymbol]
}
