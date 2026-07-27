package backendplugin

// RawJSONState distinguishes absent, JSON null, and present JSON bytes.
type RawJSONState int

const (
	// RawJSONAbsent means the field was omitted.
	RawJSONAbsent RawJSONState = iota
	// RawJSONNull means the field was explicitly JSON null.
	RawJSONNull
	// RawJSONValue means the field contains empty or non-empty JSON bytes.
	RawJSONValue
)

// RawJSON is a presence-preserving opaque JSON carrier.
type RawJSON struct {
	state RawJSONState
	data  []byte
}

// RawJSONAbsentValue returns an omitted raw JSON field.
func RawJSONAbsentValue() RawJSON { return RawJSON{state: RawJSONAbsent} }

// RawJSONNullValue returns an explicit JSON null field.
func RawJSONNullValue() RawJSON { return RawJSON{state: RawJSONNull} }

// RawJSONFromBytes returns a present JSON value (empty or non-empty).
func RawJSONFromBytes(b []byte) RawJSON {
	return RawJSON{state: RawJSONValue, data: append([]byte(nil), b...)}
}

// State returns the presence state.
func (r RawJSON) State() RawJSONState { return r.state }

// Bytes returns a copy of present JSON bytes, or nil when absent/null.
func (r RawJSON) Bytes() []byte {
	if r.state != RawJSONValue {
		return nil
	}
	return append([]byte(nil), r.data...)
}

// Validate enforces the raw JSON size bound for present values.
func (r RawJSON) Validate(maxBytes uint64) error {
	if r.state != RawJSONValue {
		return nil
	}
	return ValidateRawJSONSize(uint64(len(r.data)), maxBytes)
}
