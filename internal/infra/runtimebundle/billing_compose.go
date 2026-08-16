package runtimebundle

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

var ErrComposeBillingIncomplete = errors.New("runtimebundle: billing composition is incomplete")

type ComposeBillingInput struct {
	Store                   billing.AuthoritativeBilling
	Catalog                 *billingcompose.SnapshotCatalog
	Identity                *runtimecore.BillingIdentity // nil => PrincipalSessionIdentity
	Currency                string
	ModelMaxOutput          billingadmission.ModelMaxOutput // required
	Strict                  bool
	ConservativeCeiling     *billing.Money
	ReportsPath             string
	PostTurnBatchSize       int
	PostTurnInterval        time.Duration
	MinPreRouteHeadroomNano int64
}

func ComposeBilling(in ComposeBillingInput) (ProductionOptions, error) {
	if in.Store == nil {
		return ProductionOptions{}, fmt.Errorf("%w: store is required", ErrComposeBillingIncomplete)
	}
	if in.Catalog == nil {
		return ProductionOptions{}, fmt.Errorf("%w: catalog is required", ErrComposeBillingIncomplete)
	}
	if strings.TrimSpace(in.Currency) == "" {
		return ProductionOptions{}, fmt.Errorf("%w: currency is required", ErrComposeBillingIncomplete)
	}
	if in.ModelMaxOutput == nil {
		return ProductionOptions{}, fmt.Errorf("%w: model max-output bound is required", ErrComposeBillingIncomplete)
	}
	if !in.Catalog.HasDefaults() {
		return ProductionOptions{}, fmt.Errorf("%w: catalog defaults are required", ErrComposeBillingIncomplete)
	}
	callLegAppender, ok := in.Store.(billing.CallLegUsageAppender)
	if !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement durable call-leg usage append", ErrComposeBillingIncomplete)
	}
	exposureStore, ok := in.Store.(billing.ExposureAdmissionStore)
	if !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement atomic exposure admission", ErrComposeBillingIncomplete)
	}
	callAppender, ok := in.Store.(billing.CallUsageAppender)
	if !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement durable call usage append", ErrComposeBillingIncomplete)
	}
	if _, ok := in.Store.(billing.UsageAppendOutbox); !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement durable usage append outbox", ErrComposeBillingIncomplete)
	}
	creditStore, ok := in.Store.(billing.CreditScreenStore)
	if !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement cheap credit account read", ErrComposeBillingIncomplete)
	}
	identity := stockOrOverrideIdentity(in)
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Identity:            identity,
		Currency:            in.Currency,
		Policy:              in.Catalog.Policy,
		Pricing:             in.Catalog.RoutePricing,
		ModelMaxOutput:      in.ModelMaxOutput,
		Strict:              in.Strict,
		ConservativeCeiling: copyMoney(in.ConservativeCeiling),
		ExposureStore:       exposureStore,
	})
	if err != nil {
		return ProductionOptions{}, fmt.Errorf("%w: admission: %w", ErrComposeBillingIncomplete, err)
	}
	callResolver, err := billingcompose.NewCallRatingResolver(in.Catalog)
	if err != nil {
		return ProductionOptions{}, fmt.Errorf("%w: call rating resolver: %w", ErrComposeBillingIncomplete, err)
	}
	providerCostResolver, err := billingcompose.NewProviderCostResolver(in.Catalog, in.Currency)
	if err != nil {
		return ProductionOptions{}, fmt.Errorf("%w: provider-cost resolver: %w", ErrComposeBillingIncomplete, err)
	}
	return ProductionOptions{
		BillingCallLegAppender:      callLegAppender,
		BillingCallUsageAppender:    callAppender,
		BillingCreditGate:           billing.CheapCreditScreen{Store: creditStore, Currency: in.Currency, MinPreRouteHeadroomNano: in.MinPreRouteHeadroomNano},
		BillingExposureAdmission:    adapter,
		BillingStore:                in.Store,
		BillingReports:              in.Store,
		BillingAuthoritative:        true,
		BillingReportsPath:          in.ReportsPath,
		BillingIdentity:             identity,
		BillingCallRatingResolver:   callResolver,
		BillingProviderCostResolver: providerCostResolver,
		BillingPostTurnBatchSize:    in.PostTurnBatchSize,
		BillingPostTurnInterval:     in.PostTurnInterval,
	}, nil
}

func stockOrOverrideIdentity(in ComposeBillingInput) runtimecore.BillingIdentity {
	if in.Identity != nil {
		return *in.Identity
	}
	return billingcompose.PrincipalSessionIdentity(billingcompose.SnapshotRefFuncs{
		CustomerPricingRef: in.Catalog.CustomerPricingRef,
		ChargePolicyRef:    in.Catalog.ChargePolicyRef,
		OperatorRateRef:    in.Catalog.OperatorRateRef,
	})
}

func copyMoney(m *billing.Money) *billing.Money {
	if m == nil {
		return nil
	}
	copied := *m
	return &copied
}
