package openaicred

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ExecuteWithCredentialPool executes fn within the credential pool acquire/cooldown/retry loop.
// On FailureAuthInvalid or FailureRateLimited, the credential is marked and the loop retries with
// another available credential. On FailureRetryable, lipapi.RecoverablePreOutputError is returned.
// When pool is nil, fn is called once with an empty credential.
func ExecuteWithCredentialPool(
	ctx context.Context,
	providerID string,
	pool *credpool.Pool,
	rateLimitFallback time.Duration,
	fn func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error),
) (lipapi.ManagedEventStream, error) {
	if pool == nil {
		return fn(ctx, credpool.Credential{})
	}
	now := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cred, aerr := pool.Acquire(now, nil)
		if aerr != nil {
			if errors.Is(aerr, credpool.ErrNoUsableCredential) {
				return nil, lipapi.RecoverablePreOutputError(aerr)
			}
			return nil, fmt.Errorf("%s: %w", providerID, aerr)
		}
		stream, openErr := fn(ctx, cred)
		if openErr == nil {
			return stream, nil
		}
		kind, retryAfter := ClassifyOpenAIAPIError(openErr)
		now = time.Now()
		switch kind {
		case FailureAuthInvalid:
			pool.MarkAuthInvalid(cred.ID)
		case FailureRateLimited:
			until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, rateLimitFallback)
			pool.MarkRateLimited(cred.ID, until)
		case FailureRetryable:
			return nil, lipapi.RecoverablePreOutputError(openErr)
		default:
			return nil, openErr
		}
	}
}
