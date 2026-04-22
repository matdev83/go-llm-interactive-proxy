package routing

import (
	randv2 "math/rand/v2"
)

// v2Rng adapts *rand/v2.Rand to [Rng] (Intn delegates to IntN).
type v2Rng struct{ r *randv2.Rand }

func (v v2Rng) Intn(n int) int { return v.r.IntN(n) }

// NewSeededRng returns a deterministic, non-crypto-strength RNG for weighted routing.
// The same seed yields the same stream (PCG with a zero second word).
func NewSeededRng(seed int64) Rng {
	return v2Rng{r: randv2.New(randv2.NewPCG(uint64(seed), 0))}
}

// WrapRandV2 wraps a *rand/v2.Rand as [Rng] (for example a single shared instance in tests).
func WrapRandV2(r *randv2.Rand) Rng {
	if r == nil {
		return nil
	}
	return v2Rng{r: r}
}
