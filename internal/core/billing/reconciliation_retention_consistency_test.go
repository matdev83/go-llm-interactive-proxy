package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// TestReconciliationRetentionDerivesQuantityStatusFromItems proves the
// retained top-level quantity status/completeness is derived from item shapes
// and never trusted as a label.
func TestReconciliationRetentionDerivesQuantityStatusFromItems(t *testing.T) {
	t.Parallel()

	base := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return retentionTestResult(t, "retention-consistency-qty", 1)
	}

	t.Run("false matched label is rejected", func(t *testing.T) {
		result := base(t)
		result.Quantity.Status = ReconciliationStatusMatched
		result.Quantity.Reason = ReconciliationReasonNone
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("false complete label is rejected", func(t *testing.T) {
		result := base(t)
		result.Quantity.Complete = false
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("unknown status is rejected", func(t *testing.T) {
		result := base(t)
		result.Quantity.Status = "bogus"
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("unknown reason is rejected", func(t *testing.T) {
		result := base(t)
		result.Quantity.Reason = "not-a-reason"
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("discrepant status may not carry a reason", func(t *testing.T) {
		result := base(t)
		result.Quantity.Reason = ReconciliationReasonCoverageMismatch
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("incomparable item under discrepant top level is rejected", func(t *testing.T) {
		result := base(t)
		item := &result.Quantity.Items[0]
		item.Status = ReconciliationStatusIncomparable
		item.Reason = ReconciliationReasonSchemaMismatch
		item.SignedDelta = nil
		item.AbsoluteDelta = nil
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("valid producer result passes", func(t *testing.T) {
		if err := base(t).Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
}

// cloneRetentionJSONObject deep-copies one decoded canonical JSON object while
// preserving json.Number lexemes, so a forged payload stays byte-canonical.
func cloneRetentionJSONObject(t *testing.T, in map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal clone: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		t.Fatalf("decode clone: %v", err)
	}
	return out
}

// forgeRetentionCanonical decodes one valid canonical retention payload, applies
// a JSON-level mutation and re-encodes it, so the forged bytes stay canonical
// and only semantic revalidation can reject them.
func forgeRetentionCanonical(t *testing.T, result ReconciliationRetentionResult, mutate func(document map[string]any)) []byte {
	t.Helper()
	canonical, err := result.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode canonical: %v", err)
	}
	mutate(document)
	forged, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal forged payload: %v", err)
	}
	return forged
}

// TestReconciliationRetentionRederivesQuantityDeltasAndSourceShape proves the
// retained exact signed/absolute deltas are rederived from the retained
// evidence values with the authoritative comparator arithmetic, and that a
// comparable item carries exactly one local and one provider source. The
// canonical-bytes Parse assertion is the durable poison-row boundary: the
// forged payload is byte-canonical, so only semantic revalidation can reject
// it.
func TestReconciliationRetentionRederivesQuantityDeltasAndSourceShape(t *testing.T) {
	t.Parallel()

	base := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return retentionTestResult(t, "retention-exact-delta", 1)
	}

	// forgeCanonical mutates one comparable quantity item at the JSON level so
	// ParseReconciliationRetentionResult receives canonical bytes.
	forgeCanonical := func(t *testing.T, mutate func(item map[string]any)) []byte {
		t.Helper()
		return forgeRetentionCanonical(t, base(t), func(document map[string]any) {
			quantity, ok := document["quantity"].(map[string]any)
			if !ok {
				t.Fatalf("canonical payload has no quantity block: %#v", document)
			}
			items, ok := quantity["items"].([]any)
			if !ok || len(items) != 1 {
				t.Fatalf("canonical payload has %v quantity items, want 1", quantity["items"])
			}
			item, ok := items[0].(map[string]any)
			if !ok {
				t.Fatalf("quantity item is not an object: %#v", items[0])
			}
			mutate(item)
		})
	}

	secondEvidence := func(evidence ReconciliationQuantityEvidence, id string) ReconciliationQuantityEvidence {
		duplicate := evidence
		duplicate.Key = evidence.Key.Clone()
		duplicate.Observation.ObservationID = id
		duplicate.Observation.PayloadHash = strings.Repeat("f", 64)
		return duplicate
	}

	cases := []struct {
		name   string
		mutate func(*ReconciliationRetentionResult)
		forge  func(map[string]any)
	}{
		{
			name: "wrong signed delta",
			mutate: func(result *ReconciliationRetentionResult) {
				result.Quantity.Items[0].SignedDelta = &metering.Decimal{Coefficient: "11"}
			},
			forge: func(item map[string]any) {
				item["signed_delta"].(map[string]any)["coefficient"] = "11"
			},
		},
		{
			name: "wrong absolute delta",
			mutate: func(result *ReconciliationRetentionResult) {
				result.Quantity.Items[0].AbsoluteDelta = &metering.Decimal{Coefficient: "11"}
			},
			forge: func(item map[string]any) {
				item["absolute_delta"].(map[string]any)["coefficient"] = "11"
			},
		},
		{
			name: "wrong signed delta sign",
			mutate: func(result *ReconciliationRetentionResult) {
				result.Quantity.Items[0].SignedDelta = &metering.Decimal{Coefficient: "-10"}
			},
			forge: func(item map[string]any) {
				item["signed_delta"].(map[string]any)["coefficient"] = "-10"
			},
		},
		{
			name: "nil local value on comparable item",
			mutate: func(result *ReconciliationRetentionResult) {
				result.Quantity.Items[0].Local[0].Value = nil
			},
			forge: func(item map[string]any) {
				delete(item["local"].([]any)[0].(map[string]any), "value")
			},
		},
		{
			name: "nil provider value on comparable item",
			mutate: func(result *ReconciliationRetentionResult) {
				result.Quantity.Items[0].Provider[0].Value = nil
			},
			forge: func(item map[string]any) {
				delete(item["provider"].([]any)[0].(map[string]any), "value")
			},
		},
		{
			name: "second local source",
			mutate: func(result *ReconciliationRetentionResult) {
				item := &result.Quantity.Items[0]
				item.Local = append(item.Local, secondEvidence(item.Local[0], "retention-second-local"))
			},
			forge: func(item map[string]any) {
				local := item["local"].([]any)
				duplicate := cloneRetentionJSONObject(t, local[0].(map[string]any))
				duplicate["observation"].(map[string]any)["observation_id"] = "retention-second-local"
				duplicate["observation"].(map[string]any)["payload_hash"] = strings.Repeat("f", 64)
				item["local"] = append(local, duplicate)
			},
		},
		{
			name: "second provider source",
			mutate: func(result *ReconciliationRetentionResult) {
				item := &result.Quantity.Items[0]
				item.Provider = append(item.Provider, secondEvidence(item.Provider[0], "retention-second-provider"))
			},
			forge: func(item map[string]any) {
				provider := item["provider"].([]any)
				duplicate := cloneRetentionJSONObject(t, provider[0].(map[string]any))
				duplicate["observation"].(map[string]any)["observation_id"] = "retention-second-provider"
				duplicate["observation"].(map[string]any)["payload_hash"] = strings.Repeat("f", 64)
				item["provider"] = append(provider, duplicate)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := base(t)
			tc.mutate(&result)
			if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("Validate error = %v, want ErrInvalidReconciliationRetention", err)
			}
			if _, err := result.CanonicalJSON(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("CanonicalJSON error = %v, want ErrInvalidReconciliationRetention", err)
			}
			forged := forgeCanonical(t, tc.forge)
			if _, err := ParseReconciliationRetentionResult(forged); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("ParseReconciliationRetentionResult error = %v, want ErrInvalidReconciliationRetention", err)
			}
		})
	}

	t.Run("valid producer result passes and parses", func(t *testing.T) {
		result := base(t)
		if err := result.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		canonical, err := result.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		if _, err := ParseReconciliationRetentionResult(canonical); err != nil {
			t.Fatalf("ParseReconciliationRetentionResult: %v", err)
		}
	})
}

// TestReconciliationRetentionRequiresExactEvidenceComponentKey proves nested
// evidence must match the parent component identity fully, not only the unit.
func TestReconciliationRetentionRequiresExactEvidenceComponentKey(t *testing.T) {
	t.Parallel()

	t.Run("schema mismatch is rejected", func(t *testing.T) {
		result := retentionTestResult(t, "retention-consistency-key-schema", 1)
		result.Quantity.Items[0].Local[0].Key.SchemaID = "retention.other.schema.v1"
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("qualifier mismatch is rejected", func(t *testing.T) {
		result := retentionTestResult(t, "retention-consistency-key-qualifier", 1)
		result.Quantity.Items[0].Local[0].Key.Dimensions = []metering.Dimension{{Name: "region", Value: "eu"}}
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})
}

// TestReconciliationRetentionObservationIdentityExcludesPayloadHash proves the
// same store/observation/revision is one identity: differing payload hashes
// conflict and identical payloads are duplicates, both rejected.
func TestReconciliationRetentionObservationIdentityExcludesPayloadHash(t *testing.T) {
	t.Parallel()

	t.Run("top level differing hash conflicts", func(t *testing.T) {
		result := retentionTestResult(t, "retention-consistency-ref-hash", 1)
		duplicate := result.ObservationRefs[0]
		duplicate.PayloadHash = strings.Repeat("e", 64)
		result.ObservationRefs = append(result.ObservationRefs, duplicate)
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("top level identical duplicate is rejected", func(t *testing.T) {
		result := retentionTestResult(t, "retention-consistency-ref-dup", 1)
		result.ObservationRefs = append(result.ObservationRefs, result.ObservationRefs[0])
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("evidence differing hash conflicts", func(t *testing.T) {
		result := retentionTestResult(t, "retention-consistency-evidence-hash", 1)
		duplicate := result.Quantity.Items[0].Local[0]
		duplicate.Key = duplicate.Key.Clone()
		duplicate.Observation.PayloadHash = strings.Repeat("e", 64)
		result.Quantity.Items[0].Local = append(result.Quantity.Items[0].Local, duplicate)
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("evidence identical duplicate is rejected", func(t *testing.T) {
		result := retentionTestResult(t, "retention-consistency-evidence-dup", 1)
		duplicate := result.Quantity.Items[0].Local[0]
		duplicate.Key = duplicate.Key.Clone()
		result.Quantity.Items[0].Local = append(result.Quantity.Items[0].Local, duplicate)
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})
}

// TestReconciliationRetentionMonetaryStatusVocabularyAndConsistency proves the
// monetary rollup is validated, not trusted.
func TestReconciliationRetentionMonetaryStatusVocabularyAndConsistency(t *testing.T) {
	t.Parallel()

	base := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return retentionTestResult(t, "retention-consistency-monetary", 1)
	}

	t.Run("unknown monetary status", func(t *testing.T) {
		result := base(t)
		result.Monetary.Status = "bogus"
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("unknown monetary reason", func(t *testing.T) {
		result := base(t)
		result.Monetary.Reason = "not-a-reason"
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("false conflict label is rejected", func(t *testing.T) {
		result := base(t)
		result.Monetary.Status = MonetaryDiscrepancyConflict
		result.Monetary.Reason = MonetaryReasonQuantityEvidenceConflict
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("false partial label is rejected", func(t *testing.T) {
		result := base(t)
		result.Monetary.Status = MonetaryDiscrepancyPartial
		result.Monetary.Reason = MonetaryReasonMissingP
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("false complete label is rejected", func(t *testing.T) {
		result := base(t)
		result.Monetary.Rows[0].EndToEndCostDelta = MonetaryDiscrepancyTerm{Status: MonetaryTermMissing, Reason: MonetaryReasonMissingP}
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("valid producer result passes", func(t *testing.T) {
		if err := base(t).Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
}

// TestReconciliationRetentionRederivesMonetaryTerms proves the retained E/Q/P
// monetary decomposition is rederived from the retained valuations and the
// optional quantity comparison with the authoritative producer, and that every
// row, status, reason, cause and exact amount must match the recomputed
// Q-E/P-Q/P-E output. The canonical-bytes Parse assertion is the durable
// poison-row boundary: forged payloads are byte-canonical, so only semantic
// revalidation can reject them.
func TestReconciliationRetentionRederivesMonetaryTerms(t *testing.T) {
	t.Parallel()

	base := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return retentionTestResult(t, "retention-monetary-rederive", 1)
	}

	retentionWithMonetary := func(t *testing.T, valuations []economics.Valuation) ReconciliationRetentionResult {
		t.Helper()
		monetary, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: valuations})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		result := base(t)
		result.Monetary = &monetary
		result.Aggregate = nil
		return result
	}

	monetaryRowJSON := func(t *testing.T, document map[string]any) map[string]any {
		t.Helper()
		monetary, ok := document["monetary"].(map[string]any)
		if !ok {
			t.Fatalf("canonical payload has no monetary block: %#v", document)
		}
		rows, ok := monetary["rows"].([]any)
		if !ok || len(rows) != 1 {
			t.Fatalf("monetary rows = %v, want 1", monetary["rows"])
		}
		row, ok := rows[0].(map[string]any)
		if !ok {
			t.Fatalf("monetary row is not an object: %#v", rows[0])
		}
		return row
	}
	termJSON := func(t *testing.T, row map[string]any, name string) map[string]any {
		t.Helper()
		term, ok := row[name].(map[string]any)
		if !ok {
			t.Fatalf("term %s is not an object: %#v", name, row[name])
		}
		return term
	}
	amountDecimalJSON := func(t *testing.T, term map[string]any) map[string]any {
		t.Helper()
		amount, ok := term["amount"].(map[string]any)
		if !ok {
			t.Fatalf("term has no amount: %#v", term)
		}
		decimal, ok := amount["decimal"].(map[string]any)
		if !ok {
			t.Fatalf("term amount is not a decimal: %#v", amount)
		}
		return decimal
	}

	cases := []struct {
		name   string
		mutate func(t *testing.T, result *ReconciliationRetentionResult)
		forge  func(t *testing.T, document map[string]any)
	}{
		{
			name: "wrong metering Q-E amount",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				result.Monetary.Rows[0].MeteringCostEffect.Amount = operatorCostTestAmount(t, "USD", "0.20")
			},
			forge: func(t *testing.T, document map[string]any) {
				amountDecimalJSON(t, termJSON(t, monetaryRowJSON(t, document), "metering_cost_effect"))["coefficient"] = "2"
			},
		},
		{
			name: "wrong residual P-Q amount",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				result.Monetary.Rows[0].ReportedPriceResidual.Amount = operatorCostTestAmount(t, "USD", "0.23")
			},
			forge: func(t *testing.T, document map[string]any) {
				amountDecimalJSON(t, termJSON(t, monetaryRowJSON(t, document), "reported_price_residual"))["coefficient"] = "23"
			},
		},
		{
			name: "wrong end-to-end P-E amount",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				result.Monetary.Rows[0].EndToEndCostDelta.Amount = operatorCostTestAmount(t, "USD", "0.33")
			},
			forge: func(t *testing.T, document map[string]any) {
				amountDecimalJSON(t, termJSON(t, monetaryRowJSON(t, document), "end_to_end_cost_delta"))["coefficient"] = "33"
			},
		},
		{
			name: "complete term carries a reason",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				result.Monetary.Rows[0].MeteringCostEffect.Reason = MonetaryReasonAmountUnavailable
			},
			forge: func(t *testing.T, document map[string]any) {
				termJSON(t, monetaryRowJSON(t, document), "metering_cost_effect")["reason"] = "amount_unavailable"
			},
		},
		{
			name: "complete term carries an underived cause",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				result.Monetary.Rows[0].EndToEndCostDelta.Cause = MonetaryCauseQuantityDifference
			},
			forge: func(t *testing.T, document map[string]any) {
				termJSON(t, monetaryRowJSON(t, document), "end_to_end_cost_delta")["cause"] = "quantity_difference"
			},
		},
		{
			name: "missing term carries no reason",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				result.Monetary.Rows[0].EndToEndCostDelta = MonetaryDiscrepancyTerm{Status: MonetaryTermMissing}
				result.Monetary.Status = MonetaryDiscrepancyPartial
				result.Monetary.Reason = MonetaryReasonNone
			},
			forge: func(t *testing.T, document map[string]any) {
				monetaryRowJSON(t, document)["end_to_end_cost_delta"] = map[string]any{"status": "missing"}
				monetary := document["monetary"].(map[string]any)
				monetary["status"] = "partial"
				delete(monetary, "reason")
			},
		},
		{
			name: "missing term carries an unknown reason",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				row := &result.Monetary.Rows[0]
				row.MeteringCostEffect = MonetaryDiscrepancyTerm{Status: MonetaryTermPartial, Reason: MonetaryReasonAmountUnavailable}
				row.EndToEndCostDelta = MonetaryDiscrepancyTerm{Status: MonetaryTermMissing, Reason: "bogus"}
				result.Monetary.Status = MonetaryDiscrepancyPartial
				result.Monetary.Reason = MonetaryReasonAmountUnavailable
			},
			forge: func(t *testing.T, document map[string]any) {
				row := monetaryRowJSON(t, document)
				row["metering_cost_effect"] = map[string]any{"status": "partial", "reason": "amount_unavailable"}
				row["end_to_end_cost_delta"] = map[string]any{"status": "missing", "reason": "bogus"}
				monetary := document["monetary"].(map[string]any)
				monetary["status"] = "partial"
				monetary["reason"] = "amount_unavailable"
			},
		},
		{
			name: "incomparable term carries an unknown reason",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				row := &result.Monetary.Rows[0]
				row.MeteringCostEffect = MonetaryDiscrepancyTerm{Status: MonetaryTermIncomparable, Reason: MonetaryReasonContextMismatch}
				row.EndToEndCostDelta = MonetaryDiscrepancyTerm{Status: MonetaryTermIncomparable, Reason: "bogus"}
				result.Monetary.Status = MonetaryDiscrepancyIncomparable
				result.Monetary.Reason = MonetaryReasonContextMismatch
			},
			forge: func(t *testing.T, document map[string]any) {
				row := monetaryRowJSON(t, document)
				row["metering_cost_effect"] = map[string]any{"status": "incomparable", "reason": "context_mismatch"}
				row["end_to_end_cost_delta"] = map[string]any{"status": "incomparable", "reason": "bogus"}
				monetary := document["monetary"].(map[string]any)
				monetary["status"] = "incomparable"
				monetary["reason"] = "context_mismatch"
			},
		},
		{
			name: "partial term carries no reason",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				row := &result.Monetary.Rows[0]
				row.ReportedPriceResidual = MonetaryDiscrepancyTerm{Status: MonetaryTermPartial, Amount: row.ReportedPriceResidual.Amount}
				result.Monetary.Status = MonetaryDiscrepancyPartial
				result.Monetary.Reason = MonetaryReasonNone
			},
			forge: func(t *testing.T, document map[string]any) {
				row := monetaryRowJSON(t, document)
				term := termJSON(t, row, "reported_price_residual")
				row["reported_price_residual"] = map[string]any{"status": "partial", "amount": term["amount"]}
				monetary := document["monetary"].(map[string]any)
				monetary["status"] = "partial"
				delete(monetary, "reason")
			},
		},
		{
			name: "retained valuation total changed under copied terms",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				for i := range result.Monetary.Valuations {
					if result.Monetary.Valuations[i].Role == MonetaryRoleE {
						result.Monetary.Valuations[i].Valuation.Totals = []economics.CurrencyTotal{monetaryDecimalTotal(t, "USD", "3.00")}
					}
				}
			},
			forge: func(t *testing.T, document map[string]any) {
				monetary := document["monetary"].(map[string]any)
				valuations, ok := monetary["valuations"].([]any)
				if !ok {
					t.Fatalf("monetary valuations = %#v", monetary["valuations"])
				}
				for _, entry := range valuations {
					evidence, ok := entry.(map[string]any)
					if !ok || evidence["role"] != "e" {
						continue
					}
					valuation, ok := evidence["valuation"].(map[string]any)
					if !ok {
						t.Fatalf("valuation evidence is not an object: %#v", entry)
					}
					totals, ok := valuation["totals"].([]any)
					if !ok || len(totals) == 0 {
						t.Fatalf("valuation totals = %#v", valuation["totals"])
					}
					total, ok := totals[0].(map[string]any)
					if !ok {
						t.Fatalf("valuation total is not an object: %#v", totals[0])
					}
					amount, ok := total["amount"].(map[string]any)
					if !ok {
						t.Fatalf("valuation total has no exact amount: %#v", total)
					}
					amount["coefficient"] = "3"
				}
			},
		},
		{
			name: "row currency is not derived from the retained valuations",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				row := &result.Monetary.Rows[0]
				row.Currency = "EUR"
				for _, term := range []*MonetaryDiscrepancyTerm{&row.MeteringCostEffect, &row.ReportedPriceResidual, &row.EndToEndCostDelta} {
					term.Amount.Currency = "EUR"
				}
			},
			forge: func(t *testing.T, document map[string]any) {
				row := monetaryRowJSON(t, document)
				row["currency"] = "EUR"
				for _, name := range []string{"metering_cost_effect", "reported_price_residual", "end_to_end_cost_delta"} {
					termJSON(t, row, name)["amount"].(map[string]any)["currency"] = "EUR"
				}
			},
		},
		{
			name: "no retained valuation evidence",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				result.Monetary = &MonetaryDiscrepancyComparison{
					Status: MonetaryDiscrepancyPartial, Reason: MonetaryReasonAmountUnavailable,
					Subject: reconciliationSubject(),
				}
			},
			forge: func(t *testing.T, document map[string]any) {
				monetary := document["monetary"].(map[string]any)
				delete(monetary, "valuations")
				monetary["rows"] = []any{}
				monetary["status"] = "partial"
				monetary["reason"] = "amount_unavailable"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := base(t)
			tc.mutate(t, &result)
			if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("Validate error = %v, want ErrInvalidReconciliationRetention", err)
			}
			if _, err := result.CanonicalJSON(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("CanonicalJSON error = %v, want ErrInvalidReconciliationRetention", err)
			}
			forged := forgeRetentionCanonical(t, base(t), func(document map[string]any) { tc.forge(t, document) })
			if _, err := ParseReconciliationRetentionResult(forged); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("ParseReconciliationRetentionResult error = %v, want ErrInvalidReconciliationRetention", err)
			}
		})
	}

	t.Run("valid producer result passes and parses", func(t *testing.T) {
		result := base(t)
		if err := result.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		canonical, err := result.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		if _, err := ParseReconciliationRetentionResult(canonical); err != nil {
			t.Fatalf("ParseReconciliationRetentionResult: %v", err)
		}
	})

	t.Run("valid missing-P producer result passes and parses", func(t *testing.T) {
		result := retentionWithMonetary(t, []economics.Valuation{
			monetaryTestValuation(t, "retention-missing-p-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "retention-missing-p-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		})
		if result.Monetary.Status != MonetaryDiscrepancyPartial || result.Monetary.Reason != MonetaryReasonMissingP {
			t.Fatalf("monetary status/reason = %s/%s, want partial/missing_p", result.Monetary.Status, result.Monetary.Reason)
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		canonical, err := result.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		if _, err := ParseReconciliationRetentionResult(canonical); err != nil {
			t.Fatalf("ParseReconciliationRetentionResult: %v", err)
		}
	})

	t.Run("valid incomparable producer result passes and parses", func(t *testing.T) {
		result := retentionWithMonetary(t, []economics.Valuation{
			monetaryTestValuation(t, "retention-incomparable-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "retention-incomparable-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "EUR", "1.10")),
		})
		if result.Monetary.Status != MonetaryDiscrepancyIncomparable || result.Monetary.Reason != MonetaryReasonCurrencyMismatch {
			t.Fatalf("monetary status/reason = %s/%s, want incomparable/currency_mismatch", result.Monetary.Status, result.Monetary.Reason)
		}
		if len(result.Monetary.Rows) != 2 {
			t.Fatalf("monetary rows = %d, want 2 (EUR and USD)", len(result.Monetary.Rows))
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		canonical, err := result.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		if _, err := ParseReconciliationRetentionResult(canonical); err != nil {
			t.Fatalf("ParseReconciliationRetentionResult: %v", err)
		}
	})

	t.Run("valid partial term with amount passes and parses", func(t *testing.T) {
		result := retentionWithMonetary(t, []economics.Valuation{
			monetaryPartialTestValuation(t, "retention-partial-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "retention-partial-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
			monetaryTestValuation(t, "retention-partial-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
		})
		if result.Monetary.Status != MonetaryDiscrepancyPartial || result.Monetary.Reason != MonetaryReasonValuationIncomplete {
			t.Fatalf("monetary status/reason = %s/%s, want partial/valuation_incomplete", result.Monetary.Status, result.Monetary.Reason)
		}
		row := result.Monetary.Rows[0]
		if row.MeteringCostEffect.Amount == nil || row.MeteringCostEffect.Reason != MonetaryReasonValuationIncomplete || row.MeteringCostEffect.Cause != MonetaryCauseQuantityDifference {
			t.Fatalf("partial metering term was not preserved: %+v", row.MeteringCostEffect)
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		canonical, err := result.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		if _, err := ParseReconciliationRetentionResult(canonical); err != nil {
			t.Fatalf("ParseReconciliationRetentionResult: %v", err)
		}
	})
}

// TestReconciliationRetentionRederivesAggregateToleranceAndDiagnostics proves
// the retained aggregate rows, finding classifications and nested tolerance
// evaluations are rederived from the retained findings with the authoritative
// helpers, that the deliberate finding/diagnostic reason union admits valid
// monetary reasons such as missing_p, and that no forged aggregate invariant
// survives retention. Rule selection and limit provenance cannot be verified
// from the retained policy VersionRef alone; everything derivable from the
// retained finding, evaluation and limits is checked.
func TestReconciliationRetentionRederivesAggregateToleranceAndDiagnostics(t *testing.T) {
	t.Parallel()

	base := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return retentionTestResult(t, "retention-aggregate-rederive", 1)
	}

	retentionWithAggregate := func(t *testing.T, result ReconciliationRetentionResult, monetary MonetaryDiscrepancyComparison, policy ReconciliationTolerancePolicy) ReconciliationRetentionResult {
		t.Helper()
		findings, err := ReconciliationFindingsFromMonetaryComparison("call:unit3", monetary)
		if err != nil {
			t.Fatalf("ReconciliationFindingsFromMonetaryComparison: %v", err)
		}
		aggregate, err := AggregateReconciliationFindings(policy, findings)
		if err != nil {
			t.Fatalf("AggregateReconciliationFindings: %v", err)
		}
		result.Policy = VersionRef{ID: policy.Ref.ID, Version: policy.Ref.Version}
		// The retained monetary comparison stays the authoritative source for
		// the aggregate findings; an aggregate without its evidence is unbound.
		result.Monetary = &monetary
		result.Aggregate = &aggregate
		return result
	}

	decompose := func(t *testing.T, valuations ...economics.Valuation) MonetaryDiscrepancyComparison {
		t.Helper()
		monetary, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: valuations})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		return monetary
	}

	usdAbsolute := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.40"), nil))
	usdRelativeOnly := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, nil, toleranceLimit(t, "0.10")))
	eurOnly := toleranceTestPolicy(toleranceTestRule("eur", ReconciliationToleranceScope{Currency: "EUR"}, toleranceLimit(t, "0.40"), nil))

	matchedRetention := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		monetary := decompose(t,
			monetaryTestValuation(t, "unit3-matched-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "unit3-matched-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.00")),
		)
		return retentionWithAggregate(t, base(t), monetary, usdAbsolute)
	}

	findingOf := func(result *ReconciliationRetentionResult) *ReconciliationFinding {
		return &result.Aggregate.Findings[0]
	}
	rowOf := func(result *ReconciliationRetentionResult) *ReconciliationAggregateRow {
		return &result.Aggregate.Rows[0]
	}

	aggregateJSON := func(t *testing.T, document map[string]any) map[string]any {
		t.Helper()
		aggregate, ok := document["aggregate"].(map[string]any)
		if !ok {
			t.Fatalf("canonical payload has no aggregate block: %#v", document)
		}
		return aggregate
	}
	findingJSON := func(t *testing.T, aggregate map[string]any) map[string]any {
		t.Helper()
		findings, ok := aggregate["findings"].([]any)
		if !ok || len(findings) == 0 {
			t.Fatalf("aggregate findings = %#v", aggregate["findings"])
		}
		finding, ok := findings[0].(map[string]any)
		if !ok {
			t.Fatalf("aggregate finding is not an object: %#v", findings[0])
		}
		return finding
	}
	evaluationJSON := func(t *testing.T, finding map[string]any) map[string]any {
		t.Helper()
		evaluation, ok := finding["evaluation"].(map[string]any)
		if !ok {
			t.Fatalf("finding has no evaluation: %#v", finding)
		}
		return evaluation
	}
	rowJSON := func(t *testing.T, aggregate map[string]any) map[string]any {
		t.Helper()
		rows, ok := aggregate["rows"].([]any)
		if !ok || len(rows) == 0 {
			t.Fatalf("aggregate rows = %#v", aggregate["rows"])
		}
		row, ok := rows[0].(map[string]any)
		if !ok {
			t.Fatalf("aggregate row is not an object: %#v", rows[0])
		}
		return row
	}
	amountJSON := func(t *testing.T, container map[string]any, field string) map[string]any {
		t.Helper()
		amount, ok := container[field].(map[string]any)
		if !ok {
			t.Fatalf("%s amount = %#v", field, container[field])
		}
		return amount
	}
	amountDecimalJSON := func(t *testing.T, amount map[string]any) map[string]any {
		t.Helper()
		decimal, ok := amount["decimal"].(map[string]any)
		if !ok {
			t.Fatalf("amount is not a decimal: %#v", amount)
		}
		return decimal
	}

	cases := []struct {
		name   string
		mutate func(t *testing.T, result *ReconciliationRetentionResult)
		forge  func(t *testing.T, document map[string]any)
	}{
		{
			name: "unknown finding reason",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Reason = "bogus"
			},
			forge: func(t *testing.T, document map[string]any) {
				findingJSON(t, aggregateJSON(t, document))["reason"] = "bogus"
			},
		},
		{
			name: "unknown evaluated status",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).EvaluatedStatus = "bogus"
			},
			forge: func(t *testing.T, document map[string]any) {
				findingJSON(t, aggregateJSON(t, document))["evaluated_status"] = "bogus"
			},
		},
		{
			name: "empty evaluated status",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).EvaluatedStatus = ""
			},
			forge: func(t *testing.T, document map[string]any) {
				findingJSON(t, aggregateJSON(t, document))["evaluated_status"] = ""
			},
		},
		{
			name: "unknown evaluation reason",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).EvaluationReason = "bogus"
			},
			forge: func(t *testing.T, document map[string]any) {
				findingJSON(t, aggregateJSON(t, document))["evaluation_reason"] = "bogus"
			},
		},
		{
			name: "non-comparable finding carries an evaluation",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Status = ReconciliationStatusPartial
			},
			forge: func(t *testing.T, document map[string]any) {
				findingJSON(t, aggregateJSON(t, document))["status"] = "partial"
			},
		},
		{
			name: "non-comparable finding carries a mismatched evaluated label",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				finding := findingOf(result)
				finding.Status = ReconciliationStatusPartial
				finding.Evaluation = nil
				finding.EvaluatedStatus = ReconciliationStatusMatched
				finding.EvaluationReason = ReconciliationReasonNone
			},
			forge: func(t *testing.T, document map[string]any) {
				finding := findingJSON(t, aggregateJSON(t, document))
				finding["status"] = "partial"
				delete(finding, "evaluation")
				finding["evaluated_status"] = "matched"
				delete(finding, "evaluation_reason")
			},
		},
		{
			name: "evaluation policy id mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.PolicyID = "other-policy"
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))["policy_id"] = "other-policy"
			},
		},
		{
			name: "evaluation policy version mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.PolicyVersion = "v2"
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))["policy_version"] = "v2"
			},
		},
		{
			name: "evaluation target mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.Target.Component = "other-component"
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluation := evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))
				evaluation["target"].(map[string]any)["component"] = "other-component"
			},
		},
		{
			name: "evaluation expected amount mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.Expected = operatorCostTestAmount(t, "USD", "2.00")
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluation := evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))
				amountDecimalJSON(t, amountJSON(t, evaluation, "expected"))["coefficient"] = "2"
			},
		},
		{
			name: "evaluation reported amount mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.Reported = operatorCostTestAmount(t, "USD", "2.32")
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluation := evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))
				amountDecimalJSON(t, amountJSON(t, evaluation, "reported"))["coefficient"] = "232"
			},
		},
		{
			name: "evaluation signed delta mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.SignedDelta = operatorCostTestAmount(t, "USD", "0.31")
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluation := evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))
				amountDecimalJSON(t, amountJSON(t, evaluation, "signed_delta"))["coefficient"] = "31"
			},
		},
		{
			name: "evaluation absolute delta mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.AbsoluteDelta = operatorCostTestAmount(t, "USD", "0.31")
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluation := evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))
				amountDecimalJSON(t, amountJSON(t, evaluation, "absolute_delta"))["coefficient"] = "31"
			},
		},
		{
			name: "evaluation relative difference mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.RelativeDifference = operatorCostTestAmount(t, "USD", "0.31")
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluation := evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))
				amountDecimalJSON(t, amountJSON(t, evaluation, "relative_difference"))["coefficient"] = "31"
			},
		},
		{
			name: "evaluation relative presence lie",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.RelativePresent = false
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))["relative_present"] = false
			},
		},
		{
			name: "evaluation zero denominator lie",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.ZeroDenominator = true
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))["zero_denominator"] = true
			},
		},
		{
			name: "evaluation threshold mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.Threshold = operatorCostTestAmount(t, "USD", "0.41")
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluation := evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))
				amountDecimalJSON(t, amountJSON(t, evaluation, "threshold"))["coefficient"] = "41"
			},
		},
		{
			name: "evaluation within tolerance lie",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.WithinTolerance = false
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))["within_tolerance"] = false
			},
		},
		{
			name: "evaluation status mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.Status = ReconciliationStatusDiscrepant
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))["status"] = "discrepant"
			},
		},
		{
			name: "evaluation reason mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).Evaluation.Reason = ReconciliationComparisonReason(MonetaryReasonMissingP)
			},
			forge: func(t *testing.T, document map[string]any) {
				evaluationJSON(t, findingJSON(t, aggregateJSON(t, document)))["reason"] = "missing_p"
			},
		},
		{
			name: "estimated quality keeps an exact match",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				findingOf(result).LocalQuality = metering.QualityEstimated
			},
			forge: func(t *testing.T, document map[string]any) {
				findingJSON(t, aggregateJSON(t, document))["local_quality"] = "estimated"
			},
		},
		{
			name: "row gross absolute total mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				rowOf(result).GrossAbsoluteDiscrepancy = operatorCostTestAmount(t, "USD", "0.01")
			},
			forge: func(t *testing.T, document map[string]any) {
				row := rowJSON(t, aggregateJSON(t, document))
				amountDecimalJSON(t, amountJSON(t, row, "gross_absolute_discrepancy"))["coefficient"] = "1"
			},
		},
		{
			name: "row discrepant absolute total mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				rowOf(result).DiscrepantAbsoluteDiscrepancy = operatorCostTestAmount(t, "USD", "0.10")
			},
			forge: func(t *testing.T, document map[string]any) {
				row := rowJSON(t, aggregateJSON(t, document))
				amountDecimalJSON(t, amountJSON(t, row, "discrepant_absolute_discrepancy"))["coefficient"] = "1"
			},
		},
		{
			name: "row net signed total mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				rowOf(result).NetSignedDiscrepancy = operatorCostTestAmount(t, "USD", "0.31")
			},
			forge: func(t *testing.T, document map[string]any) {
				row := rowJSON(t, aggregateJSON(t, document))
				amountDecimalJSON(t, amountJSON(t, row, "net_signed_discrepancy"))["coefficient"] = "31"
			},
		},
		{
			name: "row affected count mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				rowOf(result).AffectedCount = 0
			},
			forge: func(t *testing.T, document map[string]any) {
				rowJSON(t, aggregateJSON(t, document))["affected_count"] = 0
			},
		},
		{
			name: "row status counts mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				rowOf(result).StatusCounts = []ReconciliationStatusCount{{Status: ReconciliationStatusMatched, Count: 1}}
			},
			forge: func(t *testing.T, document map[string]any) {
				rowJSON(t, aggregateJSON(t, document))["status_counts"] = []any{
					map[string]any{"status": "matched", "count": 1},
				}
			},
		},
		{
			name: "row missing ids mismatch",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				rowOf(result).MissingIDs = []string{"ghost-id"}
			},
			forge: func(t *testing.T, document map[string]any) {
				rowJSON(t, aggregateJSON(t, document))["missing_ids"] = []any{"ghost-id"}
			},
		},
		{
			name: "extra underived row",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				extra := *rowOf(result)
				extra.Scope = "other-scope"
				result.Aggregate.Rows = append(result.Aggregate.Rows, extra)
			},
			forge: func(t *testing.T, document map[string]any) {
				aggregate := aggregateJSON(t, document)
				rows := aggregate["rows"].([]any)
				extra := cloneRetentionJSONObject(t, rows[0].(map[string]any))
				extra["scope"] = "other-scope"
				aggregate["rows"] = append(rows, extra)
			},
		},
		{
			name: "unknown diagnostic reason",
			mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
				result.Diagnostics[0].Reason = "bogus"
			},
			forge: func(t *testing.T, document map[string]any) {
				diagnostics := document["diagnostics"].([]any)
				diagnostics[0].(map[string]any)["reason"] = "bogus"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := base(t)
			tc.mutate(t, &result)
			if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("Validate error = %v, want ErrInvalidReconciliationRetention", err)
			}
			if _, err := result.CanonicalJSON(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("CanonicalJSON error = %v, want ErrInvalidReconciliationRetention", err)
			}
			forged := forgeRetentionCanonical(t, base(t), func(document map[string]any) { tc.forge(t, document) })
			if _, err := ParseReconciliationRetentionResult(forged); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("ParseReconciliationRetentionResult error = %v, want ErrInvalidReconciliationRetention", err)
			}
		})
	}

	assertRetentionRoundTrips := func(t *testing.T, result ReconciliationRetentionResult) {
		t.Helper()
		if err := result.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		canonical, err := result.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		if _, err := ParseReconciliationRetentionResult(canonical); err != nil {
			t.Fatalf("ParseReconciliationRetentionResult: %v", err)
		}
	}

	t.Run("valid within-tolerance producer result passes and parses", func(t *testing.T) {
		assertRetentionRoundTrips(t, base(t))
	})

	t.Run("valid matched producer result passes and parses", func(t *testing.T) {
		result := matchedRetention(t)
		finding := result.Aggregate.Findings[0]
		if finding.Status != ReconciliationStatusMatched || finding.EvaluatedStatus != ReconciliationStatusMatched {
			t.Fatalf("matched finding status/evaluated = %s/%s", finding.Status, finding.EvaluatedStatus)
		}
		if finding.Evaluation == nil || !finding.Evaluation.WithinTolerance || finding.Evaluation.Status != ReconciliationStatusMatched {
			t.Fatalf("matched evaluation = %+v", finding.Evaluation)
		}
		assertRetentionRoundTrips(t, result)
	})

	t.Run("valid monetary missing_p aggregate passes and parses", func(t *testing.T) {
		monetary := decompose(t,
			monetaryTestValuation(t, "unit3-missing-p-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "unit3-missing-p-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		)
		result := retentionWithAggregate(t, base(t), monetary, usdAbsolute)
		finding := result.Aggregate.Findings[0]
		if finding.Status != ReconciliationStatusMissingProvider || finding.Reason != ReconciliationComparisonReason(MonetaryReasonMissingP) {
			t.Fatalf("missing_p finding = %s/%s", finding.Status, finding.Reason)
		}
		if finding.EvaluatedStatus != ReconciliationStatusMissingProvider || finding.EvaluationReason != ReconciliationComparisonReason(MonetaryReasonMissingP) || finding.Evaluation != nil {
			t.Fatalf("missing_p classification = %s/%s evaluation=%v", finding.EvaluatedStatus, finding.EvaluationReason, finding.Evaluation)
		}
		assertRetentionRoundTrips(t, result)
	})

	t.Run("valid monetary incomparable aggregate passes and parses", func(t *testing.T) {
		provider := monetaryTestValuation(t, "unit3-incomparable-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32"))
		provider.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "catalog://unit3/other-qualifiers/v1", ContentHash: strings.Repeat("9", 64)}
		if err := provider.Validate(); err != nil {
			t.Fatalf("provider valuation: %v", err)
		}
		monetary := decompose(t,
			monetaryTestValuation(t, "unit3-incomparable-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			provider,
		)
		result := retentionWithAggregate(t, base(t), monetary, usdAbsolute)
		finding := result.Aggregate.Findings[0]
		if finding.Status != ReconciliationStatusIncomparable || finding.Reason != ReconciliationComparisonReason(MonetaryReasonContextMismatch) {
			t.Fatalf("incomparable finding = %s/%s", finding.Status, finding.Reason)
		}
		if finding.EvaluatedStatus != ReconciliationStatusIncomparable || finding.Evaluation != nil {
			t.Fatalf("incomparable classification = %s evaluation=%v", finding.EvaluatedStatus, finding.Evaluation)
		}
		assertRetentionRoundTrips(t, result)
	})

	t.Run("valid tolerance-policy-missing aggregate passes and parses", func(t *testing.T) {
		monetary := decompose(t,
			monetaryTestValuation(t, "unit3-no-rule-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "unit3-no-rule-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
		)
		result := retentionWithAggregate(t, base(t), monetary, eurOnly)
		finding := result.Aggregate.Findings[0]
		if finding.Evaluation == nil || finding.Evaluation.Status != ReconciliationStatusPartial || finding.Evaluation.Reason != ReconciliationReasonTolerancePolicyMissing {
			t.Fatalf("policy-missing evaluation = %+v", finding.Evaluation)
		}
		if finding.Evaluation.RuleID != "" || finding.Evaluation.AbsoluteLimit != nil || finding.Evaluation.RelativeLimit != nil || finding.Evaluation.Threshold != nil {
			t.Fatalf("policy-missing evaluation retained rule material: %+v", finding.Evaluation)
		}
		assertRetentionRoundTrips(t, result)
	})

	t.Run("valid relative-only zero-denominator aggregate passes and parses", func(t *testing.T) {
		monetary := decompose(t,
			monetaryTestValuation(t, "unit3-zero-rel-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "0.00")),
			monetaryTestValuation(t, "unit3-zero-rel-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.00")),
		)
		result := retentionWithAggregate(t, base(t), monetary, usdRelativeOnly)
		finding := result.Aggregate.Findings[0]
		if finding.Evaluation == nil || finding.Evaluation.Status != ReconciliationStatusPartial || finding.Evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("relative-only zero-denominator evaluation = %+v", finding.Evaluation)
		}
		if finding.Evaluation.RuleID != "usd" || finding.Evaluation.RelativeLimit == nil || finding.Evaluation.AbsoluteLimit != nil || finding.Evaluation.Threshold != nil || !finding.Evaluation.ZeroDenominator {
			t.Fatalf("relative-only zero-denominator material = %+v", finding.Evaluation)
		}
		assertRetentionRoundTrips(t, result)
	})

	t.Run("valid absolute-limit zero-denominator aggregate passes and parses", func(t *testing.T) {
		monetary := decompose(t,
			monetaryTestValuation(t, "unit3-zero-abs-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "0.00")),
			monetaryTestValuation(t, "unit3-zero-abs-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.00")),
		)
		result := retentionWithAggregate(t, base(t), monetary, usdAbsolute)
		finding := result.Aggregate.Findings[0]
		if finding.Evaluation == nil || finding.Evaluation.Status != ReconciliationStatusDiscrepant || finding.Evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("absolute-limit zero-denominator evaluation = %+v", finding.Evaluation)
		}
		if finding.Evaluation.Threshold == nil || !finding.Evaluation.ZeroDenominator || finding.Evaluation.RelativePresent || finding.Evaluation.WithinTolerance {
			t.Fatalf("absolute-limit zero-denominator material = %+v", finding.Evaluation)
		}
		assertRetentionRoundTrips(t, result)
	})

	t.Run("monetary diagnostic reason is accepted", func(t *testing.T) {
		result := base(t)
		result.Diagnostics[0].Reason = ReconciliationComparisonReason(MonetaryReasonMissingP)
		assertRetentionRoundTrips(t, result)
	})
}

// TestReconciliationRetentionPreservesProducerConflictReason proves the
// producer-classified same-side conflict reason survives canonical retention.
// The retained quantity DTO intentionally omits observation semantics, so this
// verifies preservation of the producer's classification, not reconstruction of
// an omitted semantic.
func TestReconciliationRetentionPreservesProducerConflictReason(t *testing.T) {
	t.Parallel()

	key := reconciliationKey(metering.DirectionNone, "retention_semantics_metric", metering.UnitCount, "reconciliation.semantics.v1")
	observation := func(id, origin, semantics string) metering.Observation {
		return reconciliationObservationWithSemantics(t, id, origin, semantics,
			reconciliationMeasure(t, key, metering.QualityObserved, "method", "5"))
	}
	quantity, err := CompareComponentQuantities(
		reconciliationSide(
			observation("retention-sem-local-delta", metering.OriginLocal, metering.SemanticsDelta),
			observation("retention-sem-local-cumulative", metering.OriginLocal, metering.SemanticsCumulative),
		),
		reconciliationSide(observation("retention-sem-provider", metering.OriginProvider, metering.SemanticsDelta)),
	)
	if err != nil {
		t.Fatalf("CompareComponentQuantities: %v", err)
	}
	if len(quantity.Items) != 1 || quantity.Items[0].Reason != ReconciliationReasonConflictingLocal {
		t.Fatalf("producer comparison = %+v, want one conflicting_local item", quantity.Items)
	}

	result := retentionTestResult(t, "retention-conflict-reason", 1)
	result.Quantity = &quantity
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	canonical, err := result.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	parsed, err := ParseReconciliationRetentionResult(canonical)
	if err != nil {
		t.Fatalf("ParseReconciliationRetentionResult: %v", err)
	}
	if parsed.Quantity == nil || len(parsed.Quantity.Items) != 1 {
		t.Fatalf("retained quantity = %+v", parsed.Quantity)
	}
	item := parsed.Quantity.Items[0]
	if item.Status != ReconciliationStatusConflict || item.Reason != ReconciliationReasonConflictingLocal {
		t.Fatalf("retained item status/reason = %s/%s, want conflict/conflicting_local", item.Status, item.Reason)
	}
	if len(item.Local) != 2 || len(item.Provider) != 1 {
		t.Fatalf("retained evidence = %d local / %d provider, want both local sources retained", len(item.Local), len(item.Provider))
	}
	if item.SignedDelta != nil || item.AbsoluteDelta != nil {
		t.Fatalf("conflict item must not carry deltas: %+v", item)
	}
}
