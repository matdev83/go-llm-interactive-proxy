package journalstore

import (
	"strings"
	"testing"
)

const postgresIndexExpectedColumns = "store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id"

// TestCheckPostgresIndexDefinition covers the pure semantic comparison with
// realistic PostgreSQL pg_indexes renderings (type casts and parentheses). The
// negative cases are the reviewer's rejection scenario: an extra restriction or
// a disjunction must not satisfy verification even though the index name and the
// leading columns are unchanged.
func TestCheckPostgresIndexDefinition(t *testing.T) {
	blegPredicates := []string{"payload_kind = 'observation'"}
	candidatePredicates := []string{
		"payload_kind = 'observation'",
		"observation_subject_kind = 'statement_line'",
		"observation_origin = 'statement'",
		"observation_acquisition = 'statement_importer'",
		"authority = 'verified_statement'",
	}
	const (
		candidatePrefix = `CREATE INDEX idx_metering_facts_store_bleg_statement ON public.metering_facts USING btree (` +
			postgresIndexExpectedColumns + `) WHERE (`
		blegPrefix = `CREATE INDEX idx_metering_facts_store_bleg ON public.metering_facts USING btree (` +
			postgresIndexExpectedColumns + `) WHERE (`
	)
	cases := []struct {
		name       string
		raw        string
		columns    string
		predicates []string
		wantErr    string
	}{
		{
			name:       "bleg exact",
			raw:        blegPrefix + `payload_kind = 'observation'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
		},
		{
			name: "candidate exact",
			raw: candidatePrefix +
				`(payload_kind = 'observation'::text) AND (observation_subject_kind = 'statement_line'::text)` +
				` AND (observation_origin = 'statement'::text) AND (observation_acquisition = 'statement_importer'::text)` +
				` AND (authority = 'verified_statement'::text))`,
			columns:    postgresIndexExpectedColumns,
			predicates: candidatePredicates,
		},
		{
			name: "candidate missing conjunct rejected",
			raw: candidatePrefix +
				`(payload_kind = 'observation'::text) AND (observation_subject_kind = 'statement_line'::text)` +
				` AND (observation_origin = 'statement'::text) AND (observation_acquisition = 'statement_importer'::text))`,
			columns:    postgresIndexExpectedColumns,
			predicates: candidatePredicates,
			wantErr:    "partial predicate",
		},
		{
			name: "candidate extra conjunct rejected",
			raw: candidatePrefix +
				`(payload_kind = 'observation'::text) AND (observation_subject_kind = 'statement_line'::text)` +
				` AND (observation_origin = 'statement'::text) AND (observation_acquisition = 'statement_importer'::text)` +
				` AND (authority = 'verified_statement'::text) AND (b_leg_id = 'one-leg'::text))`,
			columns:    postgresIndexExpectedColumns,
			predicates: candidatePredicates,
			wantErr:    "partial predicate",
		},
		{
			name: "candidate disjunction rejected",
			raw: candidatePrefix +
				`(payload_kind = 'observation'::text) AND (observation_subject_kind = 'statement_line'::text)` +
				` AND (observation_origin = 'statement'::text) AND (observation_acquisition = 'statement_importer'::text)` +
				` OR (authority = 'verified_statement'::text))`,
			columns:    postgresIndexExpectedColumns,
			predicates: candidatePredicates,
			wantErr:    "partial predicate",
		},
		{
			name:       "wrong ordered columns rejected",
			raw:        `CREATE INDEX idx_metering_facts_store_bleg ON public.metering_facts USING btree (store_id, b_leg_id) WHERE (payload_kind = 'observation'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErr:    "key columns",
		},
		{
			name:       "literal case mismatch rejected",
			raw:        blegPrefix + `payload_kind = 'OBSERVATION'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErr:    "partial predicate",
		},
		{
			name:       "literal containing cast text rejected",
			raw:        blegPrefix + `payload_kind = 'observation::text'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErr:    "partial predicate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPostgresIndexDefinition(tc.raw, tc.columns, tc.predicates)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestCanonicalizePostgresIndexExpressionPreservesLiterals proves syntax
// normalization (lower-casing, cast stripping, parenthesis removal, whitespace
// collapsing) applies only outside single-quoted SQL literals; literal bytes,
// including case, embedded cast-looking text, doubled quotes, and spaces, are
// preserved exactly.
func TestCanonicalizePostgresIndexExpressionPreservesLiterals(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`payload_kind = 'OBSERVATION'::text`, `payload_kind = 'OBSERVATION'`},
		{`payload_kind = 'observation::text'::text`, `payload_kind = 'observation::text'`},
		{`note = 'it''s fine'::text`, `note = 'it''s fine'`},
		{`(a = 'X'::text) AND (b = 'y'::text)`, `a = 'X' and b = 'y'`},
	}
	for _, tc := range cases {
		if got := canonicalizePostgresIndexExpression(tc.in); got != tc.want {
			t.Errorf("canonicalizePostgresIndexExpression(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
