package routinghealth_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/routinghealth"
)

func TestCandidateHealthFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		now  func() time.Time
		want func(t *testing.T, h policy.CandidateHealth)
	}{
		{
			name: "nil config uses empty health",
			cfg:  nil,
			now:  func() time.Time { return time.Unix(0, 0) },
			want: func(t *testing.T, h policy.CandidateHealth) {
				t.Helper()
				if h == nil {
					t.Fatal("expected non-nil")
				}
				if h.UnhealthyCandidateKeys() != nil {
					t.Fatalf("expected no unhealthy keys, got %v", h.UnhealthyCandidateKeys())
				}
			},
		},
		{
			name: "circuit breaker when enabled",
			cfg: &config.Config{
				Routing: config.RoutingConfig{
					Health: config.RoutingHealthConfig{
						CircuitBreaker: config.CircuitBreakerConfig{
							Enabled:          true,
							FailureThreshold: 2,
							OpenFor:          "3s",
						},
					},
				},
			},
			now: func() time.Time { return time.Unix(100, 0) },
			want: func(t *testing.T, h policy.CandidateHealth) {
				t.Helper()
				if _, ok := h.(*policy.CircuitBreaker); !ok {
					t.Fatalf("want *policy.CircuitBreaker, got %T", h)
				}
			},
		},
		{
			name: "disabled circuit breaker uses empty",
			cfg: &config.Config{
				Routing: config.RoutingConfig{
					Health: config.RoutingHealthConfig{
						CircuitBreaker: config.CircuitBreakerConfig{Enabled: false},
					},
				},
			},
			now: nil,
			want: func(t *testing.T, h policy.CandidateHealth) {
				t.Helper()
				if h.UnhealthyCandidateKeys() != nil {
					t.Fatalf("expected no unhealthy keys when disabled, got %v", h.UnhealthyCandidateKeys())
				}
			},
		},
		{
			name: "invalid open_for still yields circuit breaker with default duration",
			cfg: &config.Config{
				Routing: config.RoutingConfig{
					Health: config.RoutingHealthConfig{
						CircuitBreaker: config.CircuitBreakerConfig{
							Enabled:          true,
							FailureThreshold: 1,
							OpenFor:          "not-a-valid-duration",
						},
					},
				},
			},
			now: func() time.Time { return time.Unix(0, 0) },
			want: func(t *testing.T, h policy.CandidateHealth) {
				t.Helper()
				if _, ok := h.(*policy.CircuitBreaker); !ok {
					t.Fatalf("want *policy.CircuitBreaker, got %T", h)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := tt.now
			if now == nil {
				now = time.Now
			}
			h := routinghealth.CandidateHealthFromConfig(tt.cfg, now)
			tt.want(t, h)
		})
	}
}
