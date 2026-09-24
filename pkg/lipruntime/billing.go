package lipruntime

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingbinding"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkbilling "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/billing"
)

// BuildWithBilling constructs a production runtime with one explicit typed
// monetary binding. Ordinary Options stay non-money; the binding carries the
// cheap credit screen, quote/admission, terminal handoff, and lifecycle
// ports. BuildWithBilling delegates to the same host construction as Build:
// one BuildHost, one Host, one Manager, one reload path.
func BuildWithBilling(ctx context.Context, opts Options, binding sdkbilling.Binding) (*Runtime, error) {
	if err := sdkbilling.ValidateBindings([]sdkbilling.Binding{binding}); err != nil {
		return nil, fmt.Errorf("lipruntime: %w", err)
	}
	adapter, err := billingbinding.NewAdapter(binding)
	if err != nil {
		return nil, fmt.Errorf("lipruntime: %w", err)
	}
	return buildCommon(ctx, opts, func(prod *runtimebundle.ProductionOptions) error {
		gate, admission, sink, identity := adapter.Chokepoints()
		prod.BillingCreditGate = gate
		prod.BillingExposureAdmission = admission
		prod.BillingTerminalUsageSink = sink
		prod.BillingIdentity = identity
		return nil
	})
}

// buildCommon is the single shared assembly behind Build and BuildWithBilling.
// Both bind one complete Host via runtimebundle.BuildHost; only the
// production-port mutation differs (nil for stock non-money builds).
func buildCommon(ctx context.Context, opts Options, apply func(*runtimebundle.ProductionOptions) error) (*Runtime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("lipruntime: nil context")
	}
	path := strings.TrimSpace(opts.ConfigPath)
	if path == "" {
		return nil, fmt.Errorf("lipruntime: empty config path")
	}
	norm, err := normalizeCanonicalOptions(opts)
	if err != nil {
		return nil, err
	}
	logOut := opts.LogWriter
	if logOut == nil {
		logOut = io.Discard
	}
	prod := stockProduction(opts, norm)
	if apply != nil {
		if err := apply(&prod); err != nil {
			return nil, err
		}
	}
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       logOut,
		Production:      prod,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		return nil, err
	}
	api, err := adaptHost(ctx, host)
	if err != nil {
		return nil, err
	}
	return &Runtime{host: api}, nil
}

// stockProduction carries the non-money descriptor-bound registrations and
// observer seams shared by every build. Monetary ports stay unset here.
func stockProduction(opts Options, norm normalizedProduction) runtimebundle.ProductionOptions {
	return runtimebundle.ProductionOptions{
		MeteringRecorder:          opts.MeteringRecorder,
		RequestRegistrations:      norm.RequestRegistrations,
		AttemptRegistrations:      norm.AttemptRegistrations,
		ConcurrencyRegistration:   norm.ConcurrencyRegistration,
		UsageSnapshotSource:       opts.UsageSnapshotSource,
		ConcurrencySnapshotSource: opts.ConcurrencySnapshotSource,
		RatingSnapshotSource:      opts.RatingSnapshotSource,
		EvidenceSink:              opts.EvidenceSink,
		MeteringQuerier:           opts.MeteringQuerier,
		TrafficObservers:          opts.TrafficObservers,
		UsageObservers:            opts.UsageObservers,
		PolicyObservers:           opts.PolicyObservers,
		FeatureHostRegistrations:  norm.FeatureHostRegistrations,
	}
}
