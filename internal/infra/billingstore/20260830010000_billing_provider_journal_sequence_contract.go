package billingstore

import (
	"context"

	"github.com/uptrace/bun"
)

func registerProviderJournalSequenceContractMigration() {
	migrations.MustRegister(providerJournalSequenceContractSchemaUp, func(context.Context, *bun.DB) error { return nil })
}
