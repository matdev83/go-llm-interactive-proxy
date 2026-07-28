package domain

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/authorityattribution"

type (
	Dimensions        = authorityattribution.Dimensions
	DimensionKey      = authorityattribution.DimensionKey
	DimensionMatcher  = authorityattribution.DimensionMatcher
	DimensionsMatcher = authorityattribution.DimensionsMatcher
)

var (
	IsSafeLabelKey      = authorityattribution.IsSafeLabelKey
	DimensionsFromScope = authorityattribution.DimensionsFromScope
)
