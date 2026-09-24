package runtimebundle

import (
	"context"
	"fmt"
)

// externalBillingBindingConfigured reports whether production carries a
// complete external monetary binding: the same runtime chokepoints as the
// internal reference composition (cheap credit screen, atomic exposure
// admission, terminal evidence sink, billing identity) but no internal
// durable store. Durability stays owned by the binding behind its terminal
// acknowledgement, so store-backed workers are never started here.
func externalBillingBindingConfigured(prod ProductionOptions) bool {
	return prod.BillingStore == nil &&
		prod.BillingCreditGate != nil &&
		prod.BillingExposureAdmission != nil &&
		prod.BillingTerminalUsageSink != nil &&
		prod.BillingIdentity.AccountID != nil
}

// configureExternalBilling starts binding-owned resources once and registers
// their close with process cleanup. It runs during process build, so a start
// failure fails the build before any generation is published and the active
// generation is never replaced. Borrowed handles are declared only and are
// never closed. The sink is structural: any terminal sink carrying the owned
// starter composes, with no import of the concrete binding package.
func configureExternalBilling(owner *processResourceOwner, prod ProductionOptions) error {
	starter, ok := prod.BillingTerminalUsageSink.(interface {
		StartOwnedResources(context.Context) error
	})
	if !ok || starter == nil {
		return fmt.Errorf("%w: external billing sink cannot start owned resources", ErrAuthoritativeBillingRequired)
	}
	if err := starter.StartOwnedResources(context.Background()); err != nil {
		return err
	}
	if closer, ok := prod.BillingTerminalUsageSink.(interface{ Close() error }); ok && closer != nil {
		owner.Own(func() error { return closer.Close() })
	}
	return nil
}
