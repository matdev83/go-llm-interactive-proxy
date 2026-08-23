package continuationsafety

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// Outcome classifies the safe continuation decision.
type Outcome string

const (
	OutcomeContinueSafe            Outcome = "continue_safe"
	OutcomeUnsafePartialToolArgs   Outcome = "unsafe_partial_tool_args"
	OutcomeUnsupportedOpaqueState  Outcome = "unsupported_opaque_state"
	OutcomeChainDepthExceeded      Outcome = "chain_depth_exceeded"
	OutcomeMaterializationExceeded Outcome = "materialization_exceeded"
)

// TailState captures normalized facts about the tail of the canonical
// trajectory derived from the last safe materialized point. It is a pure
// value type with no I/O.
type TailState struct {
	CommittedAssistantItems   []lipapi.Item
	CompletedCalls            []lipapi.Item
	CompletedResults          []lipapi.Item
	HasIncompleteToolArgs     bool
	HasUnsupportedOpaqueState bool
	PriorStatus               lipcont.RecordStatus
}

// PriorSummary carries the prior continuation record whose lineage and
// materialization bounds are to be reused for the next leg.
type PriorSummary struct {
	Record lipcont.ContinuationRecord
}

// Input is the pure policy input for safe continuation evaluation.
type Input struct {
	Prior            PriorSummary
	Tail             TailState
	SafeNativeResume bool
	Bounds           lipcont.Bounds
}

// ValidatedFacts is the traceable projection of inputs that survives into
// the decision, suitable for observability and for constructing the next
// continuation leg.
type ValidatedFacts struct {
	PreservedAssistantCount int
	PreservedToolPairs      int
	MustNotReexecute        bool
	Lineage                 lipcont.Lineage
	PreviousID              lipcont.ResponseID
	PriorStatus             lipcont.RecordStatus
	ChainDepth              int
	MaterializedBytes       int64
}

// Result is the pure decision output, including the constructed safe
// materialized trajectory when the trajectory is safe to continue.
type Result struct {
	Outcome               Outcome
	Facts                 ValidatedFacts
	SafeMaterializedItems []lipapi.Item
}

// IsSafe reports whether the outcome authorizes a new hidden B-side
// continuation leg. Only OutcomeContinueSafe is safe.
func IsSafe(o Outcome) bool {
	return o == OutcomeContinueSafe
}

func mergeBounds(b lipcont.Bounds) lipcont.Bounds {
	def := lipcont.DefaultBounds()
	if b.MaxChainDepth <= 0 {
		b.MaxChainDepth = def.MaxChainDepth
	}
	if b.MaxMaterializedItems <= 0 {
		b.MaxMaterializedItems = def.MaxMaterializedItems
	}
	if b.MaxMaterializedBytes <= 0 {
		b.MaxMaterializedBytes = def.MaxMaterializedBytes
	}
	return b
}

func buildSafeMaterializedItems(in Input) []lipapi.Item {
	prior := in.Prior.Record
	tail := in.Tail
	seen := make(map[string]struct{})
	var out []lipapi.Item
	add := func(items []lipapi.Item) {
		for _, it := range items {
			if it.ID != "" {
				if _, ok := seen[it.ID]; ok {
					continue
				}
				seen[it.ID] = struct{}{}
			}
			out = append(out, it)
		}
	}
	add(prior.InputItems)
	add(prior.OutputItems)
	add(tail.CommittedAssistantItems)
	add(tail.CompletedCalls)
	add(tail.CompletedResults)
	if out == nil {
		return nil
	}
	return lipcont.CloneItems(out)
}

func computeMaterializedBytes(prior lipcont.ContinuationRecord, safe []lipapi.Item, tail TailState) int64 {
	// New items are those tail items not already present in prior by ID.
	priorIDs := make(map[string]struct{})
	for _, it := range prior.InputItems {
		if it.ID != "" {
			priorIDs[it.ID] = struct{}{}
		}
	}
	for _, it := range prior.OutputItems {
		if it.ID != "" {
			priorIDs[it.ID] = struct{}{}
		}
	}
	var newItems []lipapi.Item
	for _, it := range tail.CommittedAssistantItems {
		if it.ID != "" {
			if _, ok := priorIDs[it.ID]; ok {
				continue
			}
		}
		newItems = append(newItems, it)
	}
	for _, it := range tail.CompletedCalls {
		if it.ID != "" {
			if _, ok := priorIDs[it.ID]; ok {
				continue
			}
		} else if it.ToolCall != nil {
			// Fallback dedup by CallID when ID empty: check any prior tool call with same CallID.
			dup := false
			for _, p := range prior.OutputItems {
				if p.ToolCall != nil && p.ToolCall.CallID == it.ToolCall.CallID {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
		}
		newItems = append(newItems, it)
	}
	for _, it := range tail.CompletedResults {
		if it.ID != "" {
			if _, ok := priorIDs[it.ID]; ok {
				continue
			}
		} else if it.ToolResult != nil {
			dup := false
			for _, p := range prior.OutputItems {
				if p.ToolResult != nil && p.ToolResult.CallID == it.ToolResult.CallID {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
		}
		newItems = append(newItems, it)
	}
	estimateNew := lipcont.EstimateItemsBytes(newItems)
	total := prior.MaterializedBytes + estimateNew
	if total < prior.MaterializedBytes {
		total = 1<<63 - 1
	}
	alt := lipcont.EstimateItemsBytes(safe)
	if alt > total {
		total = alt
	}
	if total < 0 {
		total = 1<<63 - 1
	}
	return total
}

// Evaluate walks TailState facts in precedence order: bounds checks,
// incomplete tool arguments rejection, unsupported opaque state unless
// SafeNativeResume, otherwise safe. It faithfully populates ValidatedFacts
// from inputs and builds SafeMaterializedItems reusing existing records
// rather than reconstructing from frontend bytes.
func Evaluate(in Input) Result {
	bounds := mergeBounds(in.Bounds)
	prior := in.Prior.Record
	tail := in.Tail

	facts := ValidatedFacts{
		Lineage:    prior.Lineage,
		ChainDepth: prior.ChainDepth,
	}
	// PreviousID lineage: a new continuation leg links to the immediately
	// prior record so MaterializeTrajectory's walk (cur = rec.PreviousID)
	// includes the interrupted attempt's committed items; skipping to
	// prior.PreviousID would orphan the failed leg from the trajectory.
	facts.PreviousID = prior.ID
	if tail.PriorStatus != "" {
		facts.PriorStatus = tail.PriorStatus
	} else {
		facts.PriorStatus = lipcont.EffectiveStatus(prior)
	}
	facts.PreservedAssistantCount = len(tail.CommittedAssistantItems)
	// Count preserved tool pairs by matching CallIDs present in both calls and results.
	callIDs := make(map[string]struct{})
	for _, c := range tail.CompletedCalls {
		if c.ToolCall != nil && c.ToolCall.CallID != "" {
			callIDs[c.ToolCall.CallID] = struct{}{}
		}
	}
	pairs := 0
	for _, r := range tail.CompletedResults {
		if r.ToolResult != nil && r.ToolResult.CallID != "" {
			if _, ok := callIDs[r.ToolResult.CallID]; ok {
				pairs++
			}
		}
	}
	facts.PreservedToolPairs = pairs
	facts.MustNotReexecute = pairs > 0

	safeItems := buildSafeMaterializedItems(in)
	facts.MaterializedBytes = computeMaterializedBytes(prior, safeItems, tail)

	// Precedence: bounds checks first.
	if prior.ChainDepth >= bounds.MaxChainDepth {
		return Result{Outcome: OutcomeChainDepthExceeded, Facts: facts, SafeMaterializedItems: safeItems}
	}
	if len(safeItems) > bounds.MaxMaterializedItems {
		return Result{Outcome: OutcomeMaterializationExceeded, Facts: facts, SafeMaterializedItems: safeItems}
	}
	if facts.MaterializedBytes > bounds.MaxMaterializedBytes {
		return Result{Outcome: OutcomeMaterializationExceeded, Facts: facts, SafeMaterializedItems: safeItems}
	}
	if tail.HasIncompleteToolArgs {
		return Result{Outcome: OutcomeUnsafePartialToolArgs, Facts: facts, SafeMaterializedItems: safeItems}
	}
	if tail.HasUnsupportedOpaqueState && !in.SafeNativeResume {
		return Result{Outcome: OutcomeUnsupportedOpaqueState, Facts: facts, SafeMaterializedItems: safeItems}
	}
	return Result{Outcome: OutcomeContinueSafe, Facts: facts, SafeMaterializedItems: safeItems}
}
