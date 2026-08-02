package continuation

import (
	"context"
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// MaterializeInput describes one bounded continuation materialization request.
type MaterializeInput struct {
	Store      Store
	Scope      Scope
	StartID    ResponseID
	NewInput   []lipapi.Item
	Bounds     Bounds
	Now        func() int64 // unix seconds; injectable for tests
	EstimateFn func(ContinuationRecord) int64
}

// MaterializedTrajectory is prior input, prior output, then new input in order.
type MaterializedTrajectory struct {
	// Items is the exact execution order: each parent input, its output, then
	// the next parent input, ending with NewInput.
	Items        []lipapi.Item
	InputItems   []lipapi.Item
	OutputItems  []lipapi.Item
	NewInput     []lipapi.Item
	ChainDepth   int
	TotalBytes   int64
	Lineage      Lineage
	Requirements lipapi.ProtocolRequirements
	// NativeRequirements are private replay obligations retained for routing;
	// they are never copied into the provider-facing call.
	NativeRequirements []NativeRequirement
}

func mergeBounds(b Bounds) Bounds {
	defaults := DefaultBounds()
	if b.MaxChainDepth <= 0 {
		b.MaxChainDepth = defaults.MaxChainDepth
	}
	if b.MaxMaterializedItems <= 0 {
		b.MaxMaterializedItems = defaults.MaxMaterializedItems
	}
	if b.MaxMaterializedBytes <= 0 {
		b.MaxMaterializedBytes = defaults.MaxMaterializedBytes
	}
	return b
}

// Materialize walks the previous_response_id chain, enforces depth/cycle/byte bounds,
// and returns the concatenated semantic order without invoking backends.
func Materialize(ctx context.Context, in MaterializeInput) (MaterializedTrajectory, error) {
	if ctx == nil {
		return MaterializedTrajectory{}, ErrPreviousResponseNotFound
	}
	if in.Store == nil {
		return MaterializedTrajectory{}, ErrPreviousResponseNotFound
	}
	if in.StartID.IsZero() {
		return MaterializedTrajectory{}, ErrPreviousResponseNotFound
	}
	bounds := mergeBounds(in.Bounds)
	estimate := in.EstimateFn
	if estimate == nil {
		estimate = EstimateRecordBytes
	}

	newInputBytes := EstimateItemsBytes(in.NewInput)
	if bounds.MaxMaterializedItems > 0 && len(in.NewInput) > bounds.MaxMaterializedItems {
		return MaterializedTrajectory{}, ErrMaterializedItemsExceeded
	}
	if bounds.MaxMaterializedBytes > 0 && newInputBytes > bounds.MaxMaterializedBytes {
		return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
	}

	visited := make(map[ResponseID]struct{})
	var (
		records    []ContinuationRecord
		lineage    Lineage
		reqs       lipapi.ProtocolRequirements
		depth      int
		total      int64
		items      int
		native     []NativeRequirement
		lineageSet bool
	)

	cur := in.StartID
	for {
		if err := ctx.Err(); err != nil {
			return MaterializedTrajectory{}, err
		}
		if _, ok := visited[cur]; ok {
			return MaterializedTrajectory{}, ErrCycleDetected
		}
		visited[cur] = struct{}{}
		depth++
		if depth > bounds.MaxChainDepth {
			return MaterializedTrajectory{}, ErrChainDepthExceeded
		}

		rec, err := Lookup(ctx, in.Store, in.Scope, cur)
		if err != nil {
			return MaterializedTrajectory{}, err
		}
		if !rec.Terminal {
			return MaterializedTrajectory{}, ErrPreviousResponseNotFound
		}
		if EffectiveStatus(rec) == RecordStatusIncomplete && !rec.Policy.AllowIncomplete {
			return MaterializedTrajectory{}, ErrPreviousResponseNotFound
		}
		if EffectiveStatus(rec) == RecordStatusFailed {
			return MaterializedTrajectory{}, ErrPreviousResponseNotFound
		}

		size := estimate(rec)
		if size < 0 || size > (1<<63-1)-total {
			return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
		}
		total += size
		if bounds.MaxMaterializedBytes > 0 && total > bounds.MaxMaterializedBytes {
			return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
		}

		recordItems := len(rec.InputItems) + len(rec.OutputItems)
		if recordItems > bounds.MaxMaterializedItems-items && bounds.MaxMaterializedItems > 0 {
			return MaterializedTrajectory{}, ErrMaterializedItemsExceeded
		}
		items += recordItems
		records = append(records, CloneRecord(rec))
		if !lineageSet {
			lineage = rec.Lineage
			lineageSet = true
		} else if err := mergeLineage(&lineage, rec.Lineage); err != nil {
			return MaterializedTrajectory{}, err
		}
		reqs = lipapi.UnionProtocolRequirements(reqs, rec.Requirements)
		reqs = lipapi.UnionProtocolRequirements(reqs, deriveRecordRequirements(rec))
		native = append(native, rec.NativeRequirements...)

		if rec.PreviousID.IsZero() {
			break
		}
		cur = rec.PreviousID
	}

	if err := ctx.Err(); err != nil {
		return MaterializedTrajectory{}, err
	}
	newInputBytes = EstimateItemsBytes(in.NewInput)
	if len(in.NewInput) > bounds.MaxMaterializedItems-items && bounds.MaxMaterializedItems > 0 {
		return MaterializedTrajectory{}, ErrMaterializedItemsExceeded
	}
	if newInputBytes < 0 || newInputBytes > (1<<63-1)-total {
		return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
	}
	if bounds.MaxMaterializedBytes > 0 && total+newInputBytes > bounds.MaxMaterializedBytes {
		return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
	}
	newInput := CloneItems(in.NewInput)
	items += len(newInput)
	if bounds.MaxMaterializedItems > 0 && items > bounds.MaxMaterializedItems {
		return MaterializedTrajectory{}, ErrMaterializedItemsExceeded
	}
	total += newInputBytes
	reqs = lipapi.UnionProtocolRequirements(reqs, lipapi.DeriveProtocolRequirements(lipapi.Call{Items: in.NewInput}))

	ordered := make([]lipapi.Item, 0, items)
	for i := len(records) - 1; i >= 0; i-- {
		ordered = append(ordered, CloneItems(records[i].InputItems)...)
		ordered = append(ordered, CloneItems(records[i].OutputItems)...)
	}
	ordered = append(ordered, CloneItems(newInput)...)

	return MaterializedTrajectory{
		Items:              ordered,
		InputItems:         materializedInputs(records),
		OutputItems:        materializedOutputs(records),
		NewInput:           newInput,
		ChainDepth:         depth,
		TotalBytes:         total,
		Lineage:            lineage,
		Requirements:       reqs,
		NativeRequirements: native,
	}, nil
}

func materializedInputs(records []ContinuationRecord) []lipapi.Item {
	var out []lipapi.Item
	for i := len(records) - 1; i >= 0; i-- {
		out = append(out, CloneItems(records[i].InputItems)...)
	}
	return out
}

func materializedOutputs(records []ContinuationRecord) []lipapi.Item {
	var out []lipapi.Item
	for i := len(records) - 1; i >= 0; i-- {
		out = append(out, CloneItems(records[i].OutputItems)...)
	}
	return out
}

// EstimateRecordBytes returns a deterministic byte estimate for bounds enforcement.
func EstimateRecordBytes(rec ContinuationRecord) int64 {
	serialized := RecordSize(rec)
	if rec.MaterializedBytes > serialized {
		return rec.MaterializedBytes
	}
	return serialized
}

// EstimateItemsBytes returns a conservative serialized-size estimate for items.
func EstimateItemsBytes(items []lipapi.Item) int64 {
	var n int64
	for _, it := range items {
		data, err := json.Marshal(it)
		if err != nil || int64(len(data)) > (1<<63-1)-n {
			return 1<<63 - 1
		}
		n += int64(len(data))
	}
	return n
}

func mergeLineage(dst *Lineage, next Lineage) error {
	if dst == nil {
		return ErrLineageMismatch
	}
	if !dst.ProviderBound && !next.ProviderBound {
		return nil
	}
	if !dst.ProviderBound {
		*dst = next
		return nil
	}
	if !next.ProviderBound {
		return nil
	}
	if dst.ProviderBound != next.ProviderBound || dst.ProviderID != next.ProviderID || dst.Model != next.Model || dst.CandidateKey != next.CandidateKey {
		return ErrLineageMismatch
	}
	return nil
}

func deriveRecordRequirements(rec ContinuationRecord) lipapi.ProtocolRequirements {
	items := make([]lipapi.Item, 0, len(rec.InputItems)+len(rec.OutputItems))
	items = append(items, rec.InputItems...)
	items = append(items, rec.OutputItems...)
	if len(items) == 0 {
		return lipapi.ProtocolRequirements{}
	}
	return lipapi.DeriveProtocolRequirements(lipapi.Call{Items: items})
}
