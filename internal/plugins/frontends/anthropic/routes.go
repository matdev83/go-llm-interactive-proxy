package anthropic

import (
	"net/http"

	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
)

// RouteClaims describes the routes Mount registers for one frontend owner.
func RouteClaims(ownerID string) ([]httpcontract.RouteClaim, error) {
	claim := httpcontract.RouteClaim{OwnerID: ownerID, Method: http.MethodPost, Path: "/v1/messages", Kind: httpcontract.RouteKindAnthropicMessages}
	normalized, err := claim.NormalizedClaim()
	if err != nil {
		return nil, err
	}
	return []httpcontract.RouteClaim{normalized}, nil
}
