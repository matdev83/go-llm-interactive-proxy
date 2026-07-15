package runtimebundle

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// validateRequiredAuthorityEvidenceWiring protects callers that assemble a
// runtime bundle without first running config.Validate. A required pre-work
// accounting-authority category is meaningless without both the authority
// capability and a live control-plane recorder.
func validateRequiredAuthorityEvidenceWiring(cfg *config.Config) error {
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.ControlPlane.RecordingPolicy), "required_pre_work") {
		return nil
	}
	required := false
	for _, category := range cfg.ControlPlane.RequiredCategories {
		if strings.EqualFold(strings.TrimSpace(category), "accounting_authority") {
			required = true
			break
		}
	}
	if !required {
		return nil
	}
	if !cfg.ControlPlane.Enabled {
		return fmt.Errorf("runtimebundle: control_plane.enabled: must be true when accounting_authority is required under required_pre_work")
	}
	if !cfg.Accounting.Authority.Enabled {
		return fmt.Errorf("runtimebundle: accounting.authority.enabled: must be true when accounting_authority is required under required_pre_work")
	}
	return nil
}

func disposeClosers(closers []func() error) error {
	var out error
	for _, closer := range slices.Backward(closers) {
		if err := closer(); err != nil {
			out = errors.Join(out, fmt.Errorf("runtimebundle: dispose closer: %w", err))
		}
	}
	return out
}

// withDisposedClosers disposes closers and returns err unchanged on successful
// disposal, or errors.Join(err, derr) when disposal fails. It treats a nil err
// defensively so callers can pass build errors without a separate nil check.
func withDisposedClosers(err error, closers []func() error) error {
	if derr := disposeClosers(closers); derr != nil {
		return errors.Join(err, derr)
	}
	return err
}
