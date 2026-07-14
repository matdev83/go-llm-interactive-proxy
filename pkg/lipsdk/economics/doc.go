// Package economics defines provider-neutral public contracts for money,
// independent customer/operator rating, conservative exposure assumptions,
// and immutable version snapshot references.
//
// Import DAG: authority → economics → metering (no cycles).
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, or provider SDKs.
//   - May import pkg/lipsdk/metering for EconomicPerspective and related enums.
package economics
