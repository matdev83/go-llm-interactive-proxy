package metering

import "context"

// Query is a bounded filter for listing metering facts. Unsupported or
// too-broad filters are implementation concerns; this contract only defines
// the request shape (requirement 14.4/14.5 deferred to later tasks).
type Query struct {
	Perspective EconomicPerspective `json:"perspective,omitempty"`
	Boundary    Boundary            `json:"boundary,omitempty"`
	Lifecycle   LifecycleScope      `json:"lifecycle,omitempty"`
	StreamID    string              `json:"stream_id,omitempty"`
	RequestID   string              `json:"request_id,omitempty"`
	Limit       int                 `json:"limit,omitempty"`
	Cursor      string              `json:"cursor,omitempty"`
}

// Page is one bounded page of facts plus an opaque continuation cursor.
type Page struct {
	Facts      []Fact `json:"facts"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Querier lists metering facts with bounded pagination.
type Querier interface {
	List(ctx context.Context, q Query) (Page, error)
}
