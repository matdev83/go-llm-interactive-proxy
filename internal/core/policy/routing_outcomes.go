package policy

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

type RoutingAttemptOutcomeSink interface {
	OnRoutingAttemptOutcome(candidateKey string, outcome lipapi.AttemptOutcome)
}
