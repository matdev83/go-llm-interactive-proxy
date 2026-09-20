package billing

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// RouteTariffBinding freezes the exact customer tariff material used to quote
// one candidate route: the canonical route key plus the tariff VersionRef
// identity and its canonical content hash. It detects both version changes
// and same-version content mutation. Bindings are sorted by route for
// deterministic fingerprints and replay identity.
type RouteTariffBinding struct {
	RouteID       string `json:"route_id"`
	TariffID      string `json:"tariff_id"`
	TariffVersion string `json:"tariff_version"`
	ContentHash   string `json:"content_hash"`
}

// RouteTariffKey returns the canonical binding key for a backend/model route.
// Quote and settlement must derive it identically; it deliberately excludes
// routing display params, which never change tariff resolution.
func RouteTariffKey(backendID, modelID string) string {
	return strings.TrimSpace(backendID) + ":" + strings.TrimSpace(modelID)
}

func (b RouteTariffBinding) Validate() error {
	if strings.TrimSpace(b.RouteID) == "" || strings.TrimSpace(b.TariffID) == "" || strings.TrimSpace(b.TariffVersion) == "" {
		return fmt.Errorf("%w: route tariff binding route, tariff id, and version are required", ErrExposureInvalid)
	}
	if len(b.ContentHash) != 64 || strings.ToLower(b.ContentHash) != b.ContentHash {
		return fmt.Errorf("%w: route tariff binding content hash must be lowercase SHA-256 hex", ErrExposureInvalid)
	}
	if _, err := hex.DecodeString(b.ContentHash); err != nil {
		return fmt.Errorf("%w: route tariff binding content hash: %v", ErrExposureInvalid, err)
	}
	return nil
}

// normalizeRouteTariffBindings validates, sorts by route, and rejects
// duplicate routes so admission and replay identity stay deterministic.
func normalizeRouteTariffBindings(bindings []RouteTariffBinding) ([]RouteTariffBinding, error) {
	out := make([]RouteTariffBinding, 0, len(bindings))
	for i, binding := range bindings {
		if err := binding.Validate(); err != nil {
			return nil, fmt.Errorf("%w: route tariff %d: %v", ErrExposureInvalid, i, err)
		}
		out = append(out, binding)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RouteID != out[j].RouteID {
			return out[i].RouteID < out[j].RouteID
		}
		if out[i].TariffID != out[j].TariffID {
			return out[i].TariffID < out[j].TariffID
		}
		if out[i].TariffVersion != out[j].TariffVersion {
			return out[i].TariffVersion < out[j].TariffVersion
		}
		return out[i].ContentHash < out[j].ContentHash
	})
	for i := 1; i < len(out); i++ {
		if out[i].RouteID == out[i-1].RouteID {
			return nil, fmt.Errorf("%w: duplicate route tariff binding for %q", ErrExposureInvalid, out[i].RouteID)
		}
	}
	return out, nil
}

// CheckSettledRouteTariffs compares the route tariff material actually used
// for the rated routes against the admitted frozen binding. Every used route
// must resolve to an identical admitted entry (route, tariff version, and
// content). Only admitted-empty + used-empty passes without proof: the legacy
// scalar path, which uses no tariff material on either side. A used binding
// against a legacy-empty exposure, or an omitted binding list against a rich
// exposure at any charge including zero, fails closed.
func CheckSettledRouteTariffs(admitted, used []RouteTariffBinding, actual Money) error {
	if len(admitted) == 0 && len(used) == 0 {
		return nil
	}
	if len(admitted) == 0 {
		return fmt.Errorf("%w: rated route tariffs have no admitted binding", ErrRatingSnapshotMismatch)
	}
	if len(used) == 0 {
		return fmt.Errorf("%w: settled charge %d omits the admitted route tariff attestation", ErrRatingSnapshotMismatch, actual.Nano)
	}
	byRoute := make(map[string]RouteTariffBinding, len(admitted))
	for _, binding := range admitted {
		byRoute[binding.RouteID] = binding
	}
	for _, binding := range used {
		want, ok := byRoute[binding.RouteID]
		if !ok {
			return fmt.Errorf("%w: rated route %q was not admitted", ErrRatingSnapshotMismatch, binding.RouteID)
		}
		if want != binding {
			return fmt.Errorf("%w: rated route %q tariff %s@%s hash %.8s does not match admitted %s@%s hash %.8s",
				ErrRatingSnapshotMismatch, binding.RouteID,
				binding.TariffID, binding.TariffVersion, binding.ContentHash,
				want.TariffID, want.TariffVersion, want.ContentHash)
		}
	}
	return nil
}

// AttestNoUsageRoutes builds the explicit no-usage attestation for a terminal
// repair outcome: one entry per distinct executed leg route, each carrying the
// admitted frozen tariff for that route. A leg route with no admitted binding
// fails closed; with no legs there is no defensible attestation and the
// terminal check rejects the empty list. Legacy empty admission attests
// nothing and stays compatible.
func AttestNoUsageRoutes(admitted []RouteTariffBinding, legs []CallLegUsageRecord) ([]RouteTariffBinding, error) {
	if len(admitted) == 0 {
		return nil, nil
	}
	byRoute := make(map[string]RouteTariffBinding, len(admitted))
	for _, binding := range admitted {
		byRoute[binding.RouteID] = binding
	}
	seen := make(map[string]struct{})
	var out []RouteTariffBinding
	for _, leg := range legs {
		route := RouteTariffKey(leg.BackendID, leg.ModelID)
		want, ok := byRoute[route]
		if !ok {
			return nil, fmt.Errorf("%w: no-usage leg route %q was not admitted", ErrRatingSnapshotMismatch, route)
		}
		if _, done := seen[route]; done {
			continue
		}
		seen[route] = struct{}{}
		out = append(out, want)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RouteID < out[j].RouteID })
	return out, nil
}

// ratedRouteTariffBindings derives one binding per route whose tariff material
// actually priced the valuation: each rated observation resolves to its owning
// B-leg route, and the route resolves to its model tariff or the base tariff
// with the same matching the retail rater uses. Observation refs owned by no
// B-leg (explicit proxy-service meters) carry no route tariff and are skipped.
func ratedRouteTariffBindings(legs []CallLegUsageRecord, valuation economics.Valuation, base economics.TariffSnapshot, models []ModelCustomerTariff) ([]RouteTariffBinding, error) {
	if len(valuation.InputObservations) == 0 {
		return nil, nil
	}
	type routeOwner struct {
		backend string
		model   string
	}
	owners := make(map[string]routeOwner)
	for _, leg := range legs {
		for _, observation := range leg.Observations {
			ref, err := observation.Ref(observation.Subject.StoreID)
			if err != nil {
				continue
			}
			owners[retailObservationRefIdentity(ref)] = routeOwner{backend: leg.BackendID, model: leg.ModelID}
		}
	}
	canonicalBase, err := base.Canonical()
	if err != nil {
		return nil, fmt.Errorf("%w: base tariff: %v", ErrRateInvalid, err)
	}
	canonicalModels := make([]ModelCustomerTariff, 0, len(models))
	for _, card := range models {
		canonical, err := card.Tariff.Canonical()
		if err != nil {
			return nil, fmt.Errorf("%w: model tariff %s/%s: %v", ErrRateInvalid, card.BackendID, card.ModelID, err)
		}
		canonicalModels = append(canonicalModels, ModelCustomerTariff{BackendID: card.BackendID, ModelID: card.ModelID, Tariff: canonical})
	}
	tariffsByRoute := make(map[string]economics.TariffSnapshot)
	for _, wanted := range valuation.InputObservations {
		owner, ok := owners[retailObservationRefIdentity(wanted)]
		if !ok {
			continue
		}
		route := RouteTariffKey(owner.backend, owner.model)
		if _, done := tariffsByRoute[route]; done {
			continue
		}
		tariff := canonicalBase
		if len(canonicalModels) != 0 {
			found := false
			for _, card := range canonicalModels {
				if card.BackendID == owner.backend && card.ModelID == owner.model {
					tariff = card.Tariff
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("%w: customer tariff for %s/%s", ErrRetailRateIncomplete, owner.backend, owner.model)
			}
		}
		tariffsByRoute[route] = tariff
	}
	out := make([]RouteTariffBinding, 0, len(tariffsByRoute))
	for route, tariff := range tariffsByRoute {
		if tariff.Content.ContentHash == "" {
			return nil, fmt.Errorf("%w: route %q tariff content hash is required", ErrRateInvalid, route)
		}
		out = append(out, RouteTariffBinding{
			RouteID: route, TariffID: tariff.Ref.ID, TariffVersion: tariff.Ref.Version,
			ContentHash: tariff.Content.ContentHash,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RouteID < out[j].RouteID })
	return out, nil
}
