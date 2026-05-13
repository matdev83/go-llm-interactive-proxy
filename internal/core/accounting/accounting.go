// Package accounting contains pure usage and cost accounting helpers.
package accounting

import (
	"fmt"
	"math/big"
	"strings"
)

const (
	CostSourceProviderReported = "provider_reported"
	CostSourceEstimated        = "estimated"
	CostSourceUnavailable      = "unavailable"
)

const nanosPerUnit = int64(1_000_000_000)
const tokensPerMillion = int64(1_000_000)

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

type ProviderCost struct {
	NanoUnits int64
	Currency  string
	Source    string
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

type ModelPrice struct {
	InputPer1M           int64
	CachedInputPer1M     int64
	CacheWriteInputPer1M int64
	OutputPer1M          int64
	ReasoningOutputPer1M int64
}

func DeriveTokenBreakdown(usage TokenUsage) TokenBreakdown {
	in := usage.InputTokens - usage.CacheReadTokens - usage.CacheWriteTokens
	if in < 0 {
		in = 0
	}
	out := usage.OutputTokens - usage.ReasoningTokens
	if out < 0 {
		out = 0
	}
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
	if in.ProviderCost.NanoUnits > 0 {
		cur := strings.TrimSpace(in.ProviderCost.Currency)
		if cur == "" {
			cur = catalog.currency
		}
		return CostResult{
			NanoUnits: in.ProviderCost.NanoUnits,
			Currency:  cur,
			Source:    CostSourceProviderReported,
		}
	}
	price, ok := catalog.models[catalogKey(in.Backend, in.Model)]
	if !ok {
		return CostResult{Source: CostSourceUnavailable, Unavailable: true}
	}
	br := DeriveTokenBreakdown(in.Usage)
	total := costForTokens(br.NonCachedInputTokens, price.InputPer1M)
	total += costForTokens(br.CacheReadTokens, fallbackPrice(price.CachedInputPer1M, price.InputPer1M))
	total += costForTokens(br.CacheWriteTokens, fallbackPrice(price.CacheWriteInputPer1M, price.InputPer1M))
	total += costForTokens(br.NonReasoningOutputTokens, price.OutputPer1M)
	total += costForTokens(br.ReasoningTokens, fallbackPrice(price.ReasoningOutputPer1M, price.OutputPer1M))
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
	if out.InputPer1M, err = parseNanoPrice(row.InputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("input_per_1m: %w", err)
	}
	if out.CachedInputPer1M, err = parseNanoPrice(row.CachedInputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("cached_input_per_1m: %w", err)
	}
	if out.CacheWriteInputPer1M, err = parseNanoPrice(row.CacheWriteInputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("cache_write_input_per_1m: %w", err)
	}
	if out.OutputPer1M, err = parseNanoPrice(row.OutputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("output_per_1m: %w", err)
	}
	if out.ReasoningOutputPer1M, err = parseNanoPrice(row.ReasoningOutputPer1M); err != nil {
		return ModelPrice{}, fmt.Errorf("reasoning_output_per_1m: %w", err)
	}
	return out, nil
}

func parseNanoPrice(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	rat, ok := new(big.Rat).SetString(raw)
	if !ok {
		return 0, fmt.Errorf("invalid decimal %q", raw)
	}
	if rat.Sign() < 0 {
		return 0, fmt.Errorf("must be non-negative")
	}
	rat.Mul(rat, big.NewRat(nanosPerUnit, 1))
	if !rat.IsInt() {
		return 0, fmt.Errorf("has more than 9 decimal places")
	}
	return rat.Num().Int64(), nil
}

func costForTokens(tokens, pricePer1M int64) int64 {
	if tokens <= 0 || pricePer1M <= 0 {
		return 0
	}
	return tokens * pricePer1M / tokensPerMillion
}

func fallbackPrice(v, fallback int64) int64 {
	if v != 0 {
		return v
	}
	return fallback
}

func catalogKey(backend, model string) string {
	return strings.TrimSpace(backend) + "\x00" + strings.TrimSpace(model)
}
