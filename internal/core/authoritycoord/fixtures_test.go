package authoritycoord_test

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func quotaReservation(handle string) authority.Reservation {
	return authority.Reservation{
		Handle: handle,
		Kind:   authority.ReservationQuota,
		Quantity: &metering.Quantity{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     1,
			Present:   true,
		},
	}
}

func spendReservation(handle string) authority.Reservation {
	return authority.Reservation{
		Handle: handle,
		Kind:   authority.ReservationSpend,
		Money:  &economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
	}
}
