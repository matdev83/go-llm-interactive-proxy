// Package economics defines provider-neutral public contracts for money,
// independent customer/operator rating, conservative exposure assumptions,
// immutable version snapshot references, and versioned snapshot sources.
//
// Import DAG: authority → economics → metering (no cycles).
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, or provider SDKs.
//   - May import pkg/lipsdk/metering for EconomicPerspective and related enums.
//
// Compatibility (requirement 12.8): follows metering.CompatibilityPolicy —
// Validate/IsKnown reject or detect unknown enums for local enforcement;
// additive wire decode preserves unrecognized values and ignores unknown keys.
package economics
