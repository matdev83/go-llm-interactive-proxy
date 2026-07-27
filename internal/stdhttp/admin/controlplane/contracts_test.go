package controlplane

import (
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// Compile-time proofs that concrete runtime services satisfy the narrow HTTP
// adapter contracts without forcing stdhttp to import core app packages.
var (
	_ AccountingAuthorityQueries  = (*authorityapp.Service)(nil)
	_ ConcurrencyAuthorityQueries = (*concurrencyapp.Service)(nil)
	_ cp.Queries                  = (*controlplane.QueryService)(nil)
	_ cp.ReadinessReportReader    = (*controlplane.ReadinessReportService)(nil)
)
