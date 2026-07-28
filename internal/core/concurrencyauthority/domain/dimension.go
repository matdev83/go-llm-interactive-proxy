package domain

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/authorityattribution"

type Dimensions = authorityattribution.Dimensions
type DimensionKey = authorityattribution.DimensionKey
type DimensionMatcher = authorityattribution.DimensionMatcher
type DimensionsMatcher = authorityattribution.DimensionsMatcher

var (
	IsSafeLabelKey      = authorityattribution.IsSafeLabelKey
	DimensionsFromScope = authorityattribution.DimensionsFromScope
)
