package contracttest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/contract"
)

// Config contains configuration options for executing the backend-plugin connector contract test suite.
type Config struct {
	// PluginID identifies the connector plugin under test.
	PluginID string
	// Version is the plugin implementation version.
	Version string
	// Start starts the connector plugin service. It is setup input only; Run
	// never invokes the returned service directly for certification.
	Start func(ctx context.Context) (backendplugin.Service, func(), error)
	// StartHost mounts service behind the supported host adapter and returns the
	// host-facing session used by every certification operation.
	StartHost func(ctx context.Context, service backendplugin.Service) (HostSession, func(), error)
	// Timeout specifies per-scenario execution timeout.
	Timeout time.Duration
	// FactoryKind and ConfigYAML are passed through the real Configure boundary.
	FactoryKind string
	ConfigYAML  []byte
	Secrets     backendplugin.SecretBundle
}

// CertificationResult contains connector contract certification evidence.
type CertificationResult struct {
	PluginID        string                    `json:"plugin_id"`
	Version         string                    `json:"version"`
	Passed          []string                  `json:"passed"`
	Negative        []string                  `json:"negative"`
	Executed        []string                  `json:"executed"`
	Failures        []string                  `json:"failures,omitempty"`
	Negotiated      backendplugin.Negotiation `json:"negotiated"`
	Capabilities    []lipapi.Capability       `json:"capabilities,omitempty"`
	ScenarioResults []ScenarioResult          `json:"scenario_results"`
}

// Validate checks that the artifact is complete and covers the shared SDK corpus
// exactly once. Connector-specific runners may add stronger wire assertions.
func (r CertificationResult) Validate() error {
	if r.PluginID == "" || r.Version == "" {
		return fmt.Errorf("connector certification requires plugin id and version")
	}
	if !r.Negotiated.Compatible {
		return fmt.Errorf("connector certification has incompatible negotiation")
	}
	if len(r.Failures) > 0 {
		return fmt.Errorf("connector certification has %d failures: %s", len(r.Failures), r.Failures[0])
	}
	corpus := contract.BaselineScenarioCorpus()
	if len(r.ScenarioResults) != len(corpus) {
		return fmt.Errorf("connector certification scenarios=%d want %d", len(r.ScenarioResults), len(corpus))
	}
	seen := make(map[string]bool, len(r.ScenarioResults))
	for _, scenario := range r.ScenarioResults {
		if scenario.ID == "" || seen[scenario.ID] {
			return fmt.Errorf("connector certification has duplicate/empty scenario %q", scenario.ID)
		}
		seen[scenario.ID] = true
		if !scenario.Executed {
			return fmt.Errorf("connector scenario %q has no execution evidence", scenario.ID)
		}
		if scenario.Positive && scenario.ID != string(contract.ScenarioID("cancellation")) && (scenario.FramesValidated == 0 || !scenario.Terminal) {
			return fmt.Errorf("positive connector scenario %q lacks frame/terminal evidence", scenario.ID)
		}
	}
	return nil
}

// MarshalJSON emits the validated machine-readable certification artifact.
func (r CertificationResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type alias CertificationResult
	return json.Marshal(alias(r))
}

// UnmarshalJSON accepts only complete certification artifacts.
func (r *CertificationResult) UnmarshalJSON(data []byte) error {
	type alias CertificationResult
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	candidate := CertificationResult(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

// ScenarioResult is validated machine-readable evidence for one shared semantic scenario.
type ScenarioResult struct {
	ID              string `json:"id"`
	Positive        bool   `json:"positive"`
	Executed        bool   `json:"executed"`
	Resolved        bool   `json:"resolved"`
	FramesValidated int    `json:"frames_validated"`
	Terminal        bool   `json:"terminal"`
	UsageObserved   bool   `json:"usage_observed"`
	ErrorObserved   bool   `json:"error_observed"`
	Rejected        bool   `json:"rejected"`
	Cancelled       bool   `json:"cancelled"`
}

// HostSession is the host-facing configured connector boundary. Implementations
// must forward operations through the real host adapter; a direct Service or
// ConfiguredInstance is not a certification boundary.
type HostSession interface {
	Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error)
	Execute(stream backendplugin.ExecuteStream) error
	Cancel(ctx context.Context, invocation backendplugin.Invocation) error
	Close(ctx context.Context) error
	Negotiation() backendplugin.Negotiation
}

// ScenarioExecutor is retained as a compatibility name for host-facing test
// adapters. New runners should implement HostSession.
type ScenarioExecutor interface {
	HostSession
}

// Run executes the connector contract test suite against the provided connector configuration.
func Run(t *testing.T, cfg Config) CertificationResult {
	t.Helper()

	if cfg.Start == nil {
		t.Fatalf("contracttest: Config.Start function must not be nil")
	}
	if cfg.StartHost == nil {
		t.Fatalf("contracttest: Config.StartHost must mount the service behind a host adapter")
	}

	ctx := context.Background()
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	svc, cleanup, err := cfg.Start(ctx)
	if err != nil {
		t.Fatalf("contracttest: failed to start connector plugin %q: %v", cfg.PluginID, err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	if svc == nil {
		t.Fatalf("contracttest: Start returned nil service for plugin %q", cfg.PluginID)
	}

	// StartHost owns Describe/Negotiate/Configure and returns the only boundary
	// used below. This prevents a direct Service call from certifying a connector.
	host, hostCleanup, err := cfg.StartHost(ctx, svc)
	if err != nil {
		t.Fatalf("contracttest: failed to mount host for plugin %q: %v", cfg.PluginID, err)
	}
	if hostCleanup != nil {
		defer hostCleanup()
	}
	if host == nil {
		t.Fatalf("contracttest: StartHost returned nil host for plugin %q", cfg.PluginID)
	}
	neg := host.Negotiation()
	if !neg.Compatible {
		t.Fatalf("contracttest: host negotiation rejected plugin %q: %v", cfg.PluginID, neg)
	}
	result := CertificationResult{
		PluginID: cfg.PluginID, Version: cfg.Version,
		Passed: []string{"describe-metadata", "negotiate-v1", "configure"}, Negotiated: neg,
	}
	profile, err := host.Resolve(ctx, nil)
	if err != nil {
		t.Fatalf("contracttest: Resolve failed: %v", err)
	}
	result.Passed = append(result.Passed, "configure-resolve")
	result.Capabilities = capabilityList(profile.Capabilities)
	for _, scenario := range contract.BaselineScenarioCorpus() {
		positive := scenarioApplicable(scenario, profile)
		inv := invocationForScenario(scenario)
		r := ScenarioResult{ID: string(scenario.ID), Positive: positive, Resolved: true, Executed: true}
		result.Executed = append(result.Executed, string(scenario.ID))
		if !positive {
			// Unsupported scenarios are hard negatives: no Execute call and no
			// opportunity for upstream activation.
			r.Rejected = true
			result.Negative = append(result.Negative, string(scenario.ID))
			result.ScenarioResults = append(result.ScenarioResults, r)
			continue
		}
		ms := &memoryStream{ctx: ctx, inbox: []backendplugin.ClientFrame{{Kind: backendplugin.ClientFrameStart, InstanceID: "contract", Invocation: &inv}}}
		ms.inbox = append(ms.inbox, backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "contract"})
		var runErr error
		if scenario.Feature == contract.FeatureCancellation {
			runErr = host.Cancel(ctx, inv)
			r.Cancelled = runErr == nil
		} else {
			runErr = host.Execute(ms)
		}
		if runErr != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s: %v", scenario.ID, runErr))
		}
		for _, frame := range ms.outbox {
			if err := frame.ValidateShape(); err != nil {
				result.Failures = append(result.Failures, fmt.Sprintf("%s: invalid frame: %v", scenario.ID, err))
				break
			}
			r.FramesValidated++
			if frame.Kind == backendplugin.ServerFrameTerminal {
				r.Terminal = true
			}
			if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil {
				r.UsageObserved = r.UsageObserved || frame.Event.Kind == backendplugin.EventUsageDelta
				r.ErrorObserved = r.ErrorObserved || frame.Event.Kind == backendplugin.EventError
			}
		}
		r.Rejected = !positive && runErr != nil && len(ms.outbox) == 0
		if scenario.Feature != contract.FeatureCancellation && (!r.Terminal || r.FramesValidated == 0) {
			result.Failures = append(result.Failures, fmt.Sprintf("%s: missing validated terminal stream", scenario.ID))
		}
		if scenario.Feature == contract.FeatureCancellation && !r.Cancelled {
			result.Failures = append(result.Failures, fmt.Sprintf("%s: cancellation was not observed", scenario.ID))
		}
		result.ScenarioResults = append(result.ScenarioResults, r)
	}
	if err := host.Close(ctx); err != nil {
		t.Fatalf("contracttest: close failed: %v", err)
	}
	if err := host.Close(ctx); err != nil {
		t.Fatalf("contracttest: idempotent close failed: %v", err)
	}
	result.Passed = append(result.Passed, "execute-cancel-close")
	if err := result.Validate(); err != nil {
		t.Fatalf("contracttest: invalid certification artifact: %v", err)
	}
	return result
}

type memoryStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	pos    int
}

func (m *memoryStream) Context() context.Context { return m.ctx }
func (m *memoryStream) Recv() (backendplugin.ClientFrame, error) {
	if m.pos >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.pos]
	m.pos++
	return f, nil
}

func (m *memoryStream) Send(f backendplugin.ServerFrame) error {
	m.outbox = append(m.outbox, f)
	return nil
}

func scenarioApplicable(s contract.ScenarioDescriptor, p backendplugin.ResolvedProfile) bool {
	caps := capabilityList(p.Capabilities)
	for _, required := range s.Requires.Capabilities {
		if !slices.Contains(caps, lipapi.Capability(required)) {
			return false
		}
	}
	for _, required := range s.Requires.ItemDialects {
		if !dialectPresent(p.DialectSupport.ItemDialects, required.Kind, required.Dialect) {
			return false
		}
	}
	for _, required := range s.Requires.ReasoningDialects {
		if !dialectPresent(p.DialectSupport.ReasoningDialects, required.Kind, required.Dialect) {
			return false
		}
	}
	for _, required := range s.Requires.CompactionDialects {
		if !dialectPresent(p.DialectSupport.CompactionDialects, required.Kind, required.Dialect) {
			return false
		}
	}
	for _, required := range s.Requires.ExtensionTypes {
		if !extensionPresent(p.DialectSupport.ExtensionTypes, required.Namespace, required.Type) {
			return false
		}
	}
	return true
}

func dialectPresent(have []backendplugin.DialectRequirementDTO, kind, dialect string) bool {
	for _, item := range have {
		if item.Kind == kind && item.Dialect == dialect {
			return true
		}
	}
	return false
}

func extensionPresent(have []backendplugin.ExtensionRequirementDTO, namespace, typ string) bool {
	for _, item := range have {
		if item.Namespace == namespace && item.Type == typ {
			return true
		}
	}
	return false
}

func invocationForScenario(s contract.ScenarioDescriptor) backendplugin.Invocation {
	text := "hello"
	inv := backendplugin.Invocation{
		RequestID: string(s.ID), AttemptID: string(s.ID), ALegID: "contract-a", BLegID: "contract-b",
		CanonicalModelID: "contract-model", NativeModelID: "contract-model",
		Operation: string(lipapi.OperationOpenAIChatCompletions), TransportMode: string(lipapi.TransportModeStreaming),
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}}}},
	}
	switch s.Feature {
	case contract.FeatureTools:
		inv.Tools = []backendplugin.ToolDef{{Name: "lookup", Description: "lookup", ParametersJSON: backendplugin.RawJSONFromBytes([]byte(`{"type":"object"}`))}}
	case contract.FeatureVision:
		ref := "image://contract"
		inv.Messages[0].Parts = append(inv.Messages[0].Parts, backendplugin.Part{Kind: backendplugin.PartKindImageRef, ImageRef: &ref})
	case contract.FeatureDocuments:
		ref := "file://contract"
		inv.Messages[0].Parts = append(inv.Messages[0].Parts, backendplugin.Part{Kind: backendplugin.PartKindFileRef, FileRef: &ref})
	case contract.FeatureStructuredOutput:
		mime := "application/json"
		inv.Options.ResponseMIMEType = &mime
		inv.Options.ResponseSchemaJSON = backendplugin.RawJSONFromBytes([]byte(`{"type":"object"}`))
	case contract.FeatureReasoning, contract.FeatureReasoningReplay:
		effort := "medium"
		inv.Options.ReasoningEffort = &effort
	case contract.FeatureItemReferences:
		inv.ItemAuthority = true
		inv.Items = []backendplugin.InvocationItem{{Kind: "message", ID: "item-1", Role: backendplugin.RoleUser, Content: []backendplugin.InvocationContentPart{{Kind: backendplugin.PartKindText, Text: &text}}}}
	case contract.FeatureExtensions:
		inv.Items = []backendplugin.InvocationItem{{Kind: "extension", ID: "item-extension", Extension: &backendplugin.InvocationExtensionItem{Namespace: "com.example", Type: "custom", Opaque: backendplugin.RawJSONFromBytes([]byte(`{"type":"com.example.custom"}`))}}}
	case contract.FeatureCompaction:
		inv.Operation = string(lipapi.OperationOpenAIResponses)
	}
	return inv
}

func capabilityList(c backendplugin.CapabilitySummary) []lipapi.Capability {
	var out []lipapi.Capability
	if c.Streaming {
		out = append(out, lipapi.CapabilityStreaming)
	}
	if c.Tools {
		out = append(out, lipapi.CapabilityTools)
	}
	if c.Vision {
		out = append(out, lipapi.CapabilityVision)
	}
	if c.Documents {
		out = append(out, lipapi.CapabilityDocuments)
	}
	if c.StructuredOutputs {
		out = append(out, lipapi.CapabilityStructuredOutputs)
	}
	if c.Reasoning {
		out = append(out, lipapi.CapabilityReasoning)
	}
	return out
}
