package reasoningpreservation

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
)

// CompanionPolicy is an internal composition seam for a provider companion.
// The generic feature owns state matching/restoration; composition owns any
// provider-specific trust protocol.
type CompanionPolicy struct {
	BeforeMatch  func(*lipapi.Call, request.AttemptMeta)
	AfterRestore func(context.Context, *lipapi.Call, request.AttemptMeta, MatchResult, RestoreResult)
}
