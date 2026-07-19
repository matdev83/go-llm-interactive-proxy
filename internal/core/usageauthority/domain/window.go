package domain

import (
	"fmt"
	"time"
)

type WindowAlgorithm string

const (
	WindowAlgorithmFixed WindowAlgorithm = "fixed"
)

func (a WindowAlgorithm) IsKnown() bool {
	return a == "" || a == WindowAlgorithmFixed
}

type WindowSpec struct {
	Algorithm WindowAlgorithm
	Size      time.Duration
	Anchor    time.Time
}

type WindowBounds struct {
	Start time.Time
	End   time.Time
}

type WindowKey struct {
	RuleID       string
	DimensionKey DimensionKey
	Start        time.Time
	End          time.Time
}

func (k WindowKey) String() string {
	return k.RuleID + "|" + string(k.DimensionKey) + "|" + k.Start.UTC().Format(time.RFC3339Nano) + "|" + k.End.UTC().Format(time.RFC3339Nano)
}

func (s WindowSpec) configured() bool {
	return s.Algorithm != "" || s.Size != 0 || !s.Anchor.IsZero()
}

func (s WindowSpec) Bounds(at time.Time) (WindowBounds, error) {
	if s.Algorithm == "" {
		return WindowBounds{}, fmt.Errorf("%w: algorithm required", ErrInvalidWindow)
	}
	if !s.Algorithm.IsKnown() {
		return WindowBounds{}, fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, s.Algorithm)
	}
	if s.Algorithm != WindowAlgorithmFixed {
		return WindowBounds{}, fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, s.Algorithm)
	}
	if s.Size <= 0 {
		return WindowBounds{}, fmt.Errorf("%w: size must be positive", ErrInvalidWindow)
	}

	offset := at.Sub(s.Anchor)
	step := int64(offset / s.Size)
	if offset < 0 && offset%s.Size != 0 {
		step--
	}
	start := s.Anchor.Add(time.Duration(step) * s.Size)
	return WindowBounds{Start: start, End: start.Add(s.Size)}, nil
}

func (s WindowSpec) Key(ruleID string, dims Dimensions, at time.Time) WindowKey {
	bounds, err := s.Bounds(at)
	if err != nil {
		return WindowKey{RuleID: ruleID, DimensionKey: dims.Key()}
	}
	return WindowKey{
		RuleID:       ruleID,
		DimensionKey: dims.Key(),
		Start:        bounds.Start,
		End:          bounds.End,
	}
}
