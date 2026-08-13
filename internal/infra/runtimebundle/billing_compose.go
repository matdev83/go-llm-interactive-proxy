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

// ErrComposeBillingIncomplete is returned when ComposeBilling is missing a
// required store capability, catalog default, currency, or model max-output.
var ErrComposeBillingIncomplete = errors.New("runtimebundle: billing composition is incomplete")

// ComposeBillingInput is the host-supplied assembly for one authoritative
// billing injection. ComposeBilling does not open a database.
type ComposeBillingInput struct {
	Store               billing.AuthoritativeBilling
	Catalog             *billingcompose.SnapshotCatalog
	Identity            *runtimecore.BillingIdentity // nil => PrincipalSessionIdentity
	Currency            string
	ModelMaxOutput      billingadmission.ModelMaxOutput // required
	Strict              bool
	ConservativeCeiling *billing.Money
	ReportsPath         string
	PostTurnBatchSize   int
	PostTurnInterval    time.Duration
}

// ComposeBilling validates completeness and fills ProductionOptions from an
// already-opened journal plus catalog. It does not start the post-turn worker.
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

	appender, ok := in.Store.(billing.UsageRecordAppender)
	if !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement usage-record append", ErrComposeBillingIncomplete)
	}
	if _, ok := in.Store.(billing.PostTurnStore); !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement post-turn processing", ErrComposeBillingIncomplete)
	}
	releaser, ok := in.Store.(billing.HoldReleaser)
	if !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement hold release", ErrComposeBillingIncomplete)
	}
	if _, ok := in.Store.(billing.AccountProvisioner); !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement account provisioning", ErrComposeBillingIncomplete)
	}
	lookup, ok := in.Store.(billing.AuthorizationLookup)
	if !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement authorization lookup", ErrComposeBillingIncomplete)
	}
	authStore, ok := in.Store.(billing.AuthorizationStore)
	if !ok {
		return ProductionOptions{}, fmt.Errorf("%w: store must implement authorization", ErrComposeBillingIncomplete)
	}

	identity := stockOrOverrideIdentity(in)
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store:               authStore,
		Releaser:            releaser,
		Identity:            identity,
		Currency:            in.Currency,
		Policy:              in.Catalog.Policy,
		Pricing:             in.Catalog.RoutePricing,
		ModelMaxOutput:      in.ModelMaxOutput,
		Strict:              in.Strict,
		ConservativeCeiling: copyMoney(in.ConservativeCeiling),
	})
	if err != nil {
		return ProductionOptions{}, fmt.Errorf("%w: admission: %w", ErrComposeBillingIncomplete, err)
	}
	resolver, err := billingcompose.NewRatingResolver(in.Catalog, lookup)
	if err != nil {
		return ProductionOptions{}, fmt.Errorf("%w: rating resolver: %w", ErrComposeBillingIncomplete, err)
	}

	return ProductionOptions{
		BillingTerminalHandoff:   appender,
		BillingStore:             in.Store,
		BillingReports:           in.Store,
		BillingAuthoritative:     true,
		BillingReportsPath:       in.ReportsPath,
		BillingIdentity:          identity,
		BillingRatingResolver:    resolver,
		BillingPostTurnBatchSize: in.PostTurnBatchSize,
		BillingPostTurnInterval:  in.PostTurnInterval,
		BillingAdmission:         adapter,
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

// copyMoney defensively copies a caller-owned Money pointer so the adapter never
// observes later caller mutation. Nil stays nil.
func copyMoney(m *billing.Money) *billing.Money {
	if m == nil {
		return nil
	}
	copied := *m
	return &copied
}
