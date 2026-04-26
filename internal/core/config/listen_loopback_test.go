package config_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestIsExplicitLoopbackListenAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		want bool
	}{
		{":8080", false},
		{"0.0.0.0:8080", false},
		{"[::]:8080", false},
		{"127.0.0.1:8080", true},
		{"127.0.0.1", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"::1", true},
		{"localhost", true},
		{"", false},
	}
	for _, tc := range cases {
		if got := config.IsExplicitLoopbackListenAddress(tc.addr); got != tc.want {
			t.Fatalf("addr=%q: want %v got %v", tc.addr, tc.want, got)
		}
	}
}
