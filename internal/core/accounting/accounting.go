// Package accounting contains pure usage and cost accounting helpers.
package accounting

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strings"
)

const (
	CostSourceProviderReported = "provider_reported"
	CostSourceEstimated        = "estimated"
	CostSourceUnavailable      = "unavailable"
)

const (
	nanosPerUnit     = int64(1_000_000_000)
	tokensPerMillion = int64(1_000_000)
)

type TokenUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
}

type TokenBreakdown struct {
	TokenUsage
	NonCachedInputTokens     int64
	NonReasoningOutputTokens int64
}

// ProviderCost is an optional authoritative provider-reported cost.
// Present distinguishes authoritative zero from an absent cost.
type ProviderCost struct {
	NanoUnits int64
	Currency  string
	Source    string
	Present   bool
}

type CostInput struct {
	Backend      string
	Model        string
	Usage        TokenUsage
	ProviderCost ProviderCost
}

type CostResult struct {
	NanoUnits      int64
	Currency       string
	Source         string
	CatalogVersion string
	Unavailable    bool
}

type PriceCatalogConfig struct {
	Version  string
	Currency string
	Models   []ModelPriceConfig
}

type ModelPriceConfig struct {
	Backend              string
	Model                string
	InputPer1M           string
	CachedInputPer1M     string
	CacheWriteInputPer1M string
	OutputPer1M          string
	ReasoningOutputPer1M string
}

type PriceCatalog struct {
	version  string
	currency string
	models   map[string]ModelPrice
}

// OptionalNanoRate is a catalog rate that may be unspecified (absent) or
// explicitly present, including an authoritative zero.
type OptionalNanoRate struct {
	NanoUnits int64
	Present   bool
}

type ModelPrice struct {
	InputPer1M           int64
	CachedInputPer1M     OptionalNanoRate
	CacheWriteInputPer1M OptionalNanoRate
	OutputPer1M          int64
	ReasoningOutputPer1M OptionalNanoRate
}

func DeriveTokenBreakdown(usage TokenUsage) TokenBreakdown {
	in := max(usage.InputTokens-usage.CacheReadTokens-usage.CacheWriteTokens, 0)
	out := max(usage.OutputTokens-usage.ReasoningTokens, 0)
	return TokenBreakdown{
		TokenUsage:               usage,
		NonCachedInputTokens:     in,
		NonReasoningOutputTokens: out,
	}
}

func NewPriceCatalog(cfg PriceCatalogConfig) (PriceCatalog, error) {
	cur := strings.TrimSpace(cfg.Currency)
	if cur == "" {
		cur = "USD"
	}
	out := PriceCatalog{
		version:  strings.TrimSpace(cfg.Version),
		currency: cur,
		models:   make(map[string]ModelPrice, len(cfg.Models)),
	}
	for i, row := range cfg.Models {
		backend := strings.TrimSpace(row.Backend)
		model := strings.TrimSpace(row.Model)
		if backend == "" {
			return PriceCatalog{}, fmt.Errorf("accounting: models[%d].backend required", i)
		}
		if model == "" {
			return PriceCatalog{}, fmt.Errorf("accounting: models[%d].model required", i)
		}
		price, err := parseModelPrice(row)
		if err != nil {
			return PriceCatalog{}, fmt.Errorf("accounting: models[%d]: %w", i, err)
		}
		out.models[catalogKey(backend, model)] = price
	}
	return out, nil
}

func EstimateCost(in CostInput, catalog PriceCatalog) CostResult {
	if in.ProviderCost.Present {
		cur := strings.TrimSpace(in.ProviderCost.Currency)
		if cur == "" {
			cur = catalog.currency
		}
		source := strings.TrimSpace(in.ProviderCost.Source)
		if source == "" {
			source = CostSourceProviderReported
		}
		return CostResult{
			NanoUnits: in.ProviderCost.NanoUnits,
			Currency:  cur,
			Source:    source,
		}
	}
	price, ok := catalog.models[catalogKey(in.Backend, in.Model)]
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	br := DeriveTokenBreakdown(in.Usage)
	total, ok := costForTokensChecked(br.NonCachedInputTokens, price.InputPer1M)
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	line, ok := costForTokensChecked(br.CacheReadTokens, effectiveRate(price.CachedInputPer1M, price.InputPer1M))
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	total, ok = addMoneyChecked(total, line)
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	line, ok = costForTokensChecked(br.CacheWriteTokens, effectiveRate(price.CacheWriteInputPer1M, price.InputPer1M))
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	total, ok = addMoneyChecked(total, line)
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	line, ok = costForTokensChecked(br.NonReasoningOutputTokens, price.OutputPer1M)
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	total, ok = addMoneyChecked(total, line)
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	line, ok = costForTokensChecked(br.ReasoningTokens, effectiveRate(price.ReasoningOutputPer1M, price.OutputPer1M))
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	total, ok = addMoneyChecked(total, line)
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	return CostResult{
		NanoUnits:      total,
		Currency:       catalog.currency,
		Source:         CostSourceEstimated,
		CatalogVersion: catalog.version,
	}
}

func parseModelPrice(row ModelPriceConfig) (ModelPrice, error) {
	var out ModelPrice
	var err error
	if out.InputPer1M, err = parseRequiredNanoPrice(row.InputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("input_per_1m: %w", err)
	}
	if out.CachedInputPer1M, err = parseOptionalNanoPrice(row.CachedInputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("cached_input_per_1m: %w", err)
	}
	if out.CacheWriteInputPer1M, err = parseOptionalNanoPrice(row.CacheWriteInputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("cache_write_input_per_1m: %w", err)
	}
	if out.OutputPer1M, err = parseRequiredNanoPrice(row.OutputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("output_per_1m: %w", err)
	}
	if out.ReasoningOutputPer1M, err = parseOptionalNanoPrice(row.ReasoningOutputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("reasoning_output_per_1m: %w", err)
	}
	return out, nil
}

func parseRequiredNanoPrice(raw string) (int64, error) {
	rate, err := parseOptionalNanoPrice(raw)
	if err != nil {
		return 0, err
	}
	// Empty required rate remains 0 (absent treated as free/zero for base rates).
	return rate.NanoUnits, nil
}

func parseOptionalNanoPrice(raw string) (OptionalNanoRate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return OptionalNanoRate{}, nil
	}
	rat, ok := new(big.Rat).SetString(raw)
	if !ok {
		return OptionalNanoRate{}, fmt.Errorf("invalid decimal %q", raw)
	}
	if rat.Sign() < 0 {
		return OptionalNanoRate{}, fmt.Errorf("must be non-negative")
	}
	rat.Mul(rat, big.NewRat(nanosPerUnit, 1))
	if !rat.IsInt() {
		return OptionalNanoRate{}, fmt.Errorf("has more than 9 decimal places")
	}
	return OptionalNanoRate{NanoUnits: rat.Num().Int64(), Present: true}, nil
}

// costForTokensChecked multiplies tokens by a per-1M nano rate with overflow detection.
func costForTokensChecked(tokens, pricePer1M int64) (int64, bool) {
	if tokens <= 0 || pricePer1M <= 0 {
		return 0, true
	}
	hi, lo := bits.Mul64(uint64(tokens), uint64(pricePer1M))
	div := uint64(tokensPerMillion)
	if hi >= div {
		return 0, false
	}
	q, _ := bits.Div64(hi, lo, div)
	if q > math.MaxInt64 {
		return 0, false
	}
	return int64(q), true
}

func addMoneyChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	sum, carry := bits.Add64(uint64(a), uint64(b), 0)
	if carry != 0 || sum > math.MaxInt64 {
		return 0, false
	}
	return int64(sum), true
}

// SubMoneyChecked subtracts non-negative money amounts without underflow.
func SubMoneyChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a < b {
		return 0, false
	}
	return a - b, true
}

func effectiveRate(rate OptionalNanoRate, fallback int64) int64 {
	if rate.Present {
		return rate.NanoUnits
	}
	return fallback
}

func catalogKey(backend, model string) string {
	return strings.TrimSpace(backend) + "\x00" + strings.TrimSpace(model)
}
