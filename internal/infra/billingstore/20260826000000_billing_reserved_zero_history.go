package billingstore

import (
	"context"

	"github.com/uptrace/bun"
)

// registerReservedZeroHistoryMarker preserves the old migration identity for
// fresh schemas without running its historical destructive implementation.
// Brownfield residue is proved by the later forward removal migration.
func registerReservedZeroHistoryMarker() {
	migrations.MustRegister(func(context.Context, *bun.DB) error { return nil }, func(context.Context, *bun.DB) error { return nil })
}
