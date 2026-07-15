package journalstore

import "errors"

// ErrIdentityCollision is returned when Append sees the same StreamID+FactID
// (source_event_key) with a different Sequence than the stored fact
// (requirements 3.1, 3.4, 13.4).
var ErrIdentityCollision = errors.New("metering/journalstore: fact identity collision")

// ErrQueryTooBroad is returned when List lacks a required bound (StreamID or
// RequestID) so the store cannot safely page (requirement 14.4/13.x).
var ErrQueryTooBroad = errors.New("metering/journalstore: query too broad; require stream_id or request_id")
