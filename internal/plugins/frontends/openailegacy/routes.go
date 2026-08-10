package openailegacy

import (
	"net/http"

	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
)

// RouteClaims describes the route Mount registers for one frontend owner.
func RouteClaims(ownerID string) ([]httpcontract.RouteClaim, error) {
	claim := httpcontract.RouteClaim{OwnerID: ownerID, Method: http.MethodPost, Path: "/v1/chat/completions", Kind: httpcontract.RouteKindOpenAIChatCompletions}
	normalized, err := claim.NormalizedClaim()
	if err != nil {
		return nil, err
	}
	return []httpcontract.RouteClaim{normalized}, nil
}
