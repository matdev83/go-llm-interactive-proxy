package controlplane

import (
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// AdaptAccountingAuthorityQueries projects a concrete usage-authority service onto the
// narrow HTTP query contract. A nil or typed-nil pointer becomes a true nil interface
// so mount and handler `== nil` checks leave optional routes disabled.
func AdaptAccountingAuthorityQueries(svc *authorityapp.Service) AccountingAuthorityQueries {
	if svc == nil {
		return nil
	}
	return svc
}

// AdaptConcurrencyAuthorityQueries projects a concrete concurrency-authority service onto
// the narrow HTTP query contract. A nil or typed-nil pointer becomes a true nil interface.
func AdaptConcurrencyAuthorityQueries(svc *concurrencyapp.Service) ConcurrencyAuthorityQueries {
	if svc == nil {
		return nil
	}
	return svc
}

// AdaptControlPlaneQueries projects a concrete query service onto lipcp.Queries.
// A nil or typed-nil pointer becomes a true nil interface.
func AdaptControlPlaneQueries(q *controlplane.QueryService) cp.Queries {
	if q == nil {
		return nil
	}
	return q
}

// AdaptReadinessReport projects a concrete readiness service onto ReadinessReportReader.
// A nil or typed-nil pointer becomes a true nil interface.
func AdaptReadinessReport(r *controlplane.ReadinessReportService) cp.ReadinessReportReader {
	if r == nil {
		return nil
	}
	return r
}
