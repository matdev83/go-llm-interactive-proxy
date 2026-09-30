//go:build integration

package billing_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Exhaustive three-node products belong to certification. Shared helpers and
// named regressions remain in the default unit suite.
func TestSchemaModelStructureSweep(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := newSMReport()
	graphs := smGraphs()
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			report.skipped++
			t.Logf("validator skipped graph [%s]: %v", smOptsName(opts), err)
			continue
		}
		valid = append(valid, opts)
	}
	evidence := smAllEvidence()
	seams := review5beSeams()
	report.graphCount = len(graphs)
	report.evidenceCount = len(evidence)
	report.seams = len(seams)

	t.Cleanup(func() { report.finish(t, "structure", time.Since(start)) })

	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			rels := smRelationships(opts)
			resolved := f356Schema(t, fmt.Sprintf("sm-structure-%d", gi), smFreeRules(t), rels)
			for _, ev := range evidence {
				for _, seam := range seams {
					smEvaluateCase(t, report, "structure", seam, resolved, rels, opts, ev, false)
				}
			}
		})
	}
}

func TestSchemaModelCommercialSweep(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := newSMReport()
	graphs := smGraphs()
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			report.skipped++
			continue
		}
		valid = append(valid, opts)
	}
	evidence := smAllEvidence()
	seams := review5beSeams()
	report.graphCount = len(graphs)
	report.evidenceCount = len(evidence)
	report.seams = len(seams)

	t.Cleanup(func() { report.finish(t, "commercial", time.Since(start)) })

	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			rels := smRelationships(opts)
			resolved := f356Schema(t, fmt.Sprintf("sm-commercial-%d", gi), smCommercialRules(t), rels)
			for _, ev := range evidence {
				for _, seam := range seams {
					smEvaluateCase(t, report, "commercial", seam, resolved, rels, opts, ev, true)
				}
			}
		})
	}
}
