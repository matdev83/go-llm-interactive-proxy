package systemonecompat

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatmode"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

// ID is the configurable System One protocol-family backend kind.
const ID = "custom-systemone-compatible"

// BuildCompatible constructs a decision-only backend using strict compatible
// configuration and static model inventory. Credentials are resolved per call.
func BuildCompatible(instanceID string, n yaml.Node, client *http.Client) (execbackend.Backend, error) {
	return BuildCompatibleWithHeaders(instanceID, n, client, nil)
}

// BuildCompatibleWithHeaders binds validated, operator-owned profile headers.
// The header map is cloned once; no caller header is consulted during execution.
func BuildCompatibleWithHeaders(instanceID string, n yaml.Node, client *http.Client, headers http.Header) (execbackend.Backend, error) {
	headers = headers.Clone()
	cfg, err := config.DecodeCompatibleModeConfig(instanceID, ID, n)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if err := pluginreg.ValidatePrefixSyntax(cfg.BackendPrefix); err != nil {
		return execbackend.Backend{}, err
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return execbackend.Backend{}, errors.New("system one requires an HTTP(S) base URL without credentials, query or fragment")
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/systemone"
	if client == nil {
		client = httpclient.Standard()
	}
	caps := lipapi.NewBackendCaps(lipapi.CapabilityDecisions)
	be := execbackend.Backend{
		Caps: caps, BackendPrefixes: []string{cfg.BackendPrefix},
		TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
			Operation: lipapi.OperationDecisionEvaluate, Modes: []lipapi.TransportMode{lipapi.TransportModeNonStreaming},
		}),
		ResolveCaps: func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendCaps { return caps },
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, lipapi.ErrNilContext
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if call.Decision == nil || call.Invocation.Operation != lipapi.OperationDecisionEvaluate || call.Invocation.TransportMode == lipapi.TransportModeStreaming {
				return nil, &lipapi.RejectError{Reason: "system one supports only non-streaming decision.evaluate"}
			}
			if err := call.Validate(); err != nil {
				return nil, err
			}
			key := compatmode.FirstAPIKey(compatmode.ResolveEnvAPIKeys(cfg.APIKeyEnvVarRoot))
			events, err := evaluateWire(ctx, client, endpoint, key, cand.Primary.Model, *call.Decision, headers)
			if err != nil {
				classified := classifyError(ctx, err)
				if evidence, ok := errors.AsType[*failureUsageError](err); ok {
					return &failureUsageStream{FixedEventStream: lipapi.NewFixedEventStream(nil), failure: classified, evidence: []lipapi.Event{evidence.usage}}, nil
				}
				return nil, classified
			}
			return lipapi.NewFixedEventStream(events), nil
		},
	}
	be, err = compatmode.ApplyStaticModelInventory(be, cfg.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if be.ModelInventory == nil {
		return execbackend.Backend{}, errors.New("system one requires static model inventory")
	}
	return compatmode.ApplyRuntimePolicy(be, cfg)
}

// LifecycleSystemOneCompatible is the standard distribution factory seam.
func LifecycleSystemOneCompatible(instanceID string, n yaml.Node, client *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildCompatible(instanceID, n, client)
	return pluginreg.BackendBuildResult{Backend: be}, err
}
