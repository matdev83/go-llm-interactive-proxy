package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DecodeLine parses and validates one NDJSON bridge frame.
// Line must not include a trailing newline. Oversized input fails before JSON decode.
func DecodeLine(line []byte) (*Frame, error) {
	if len(line) > MaxFrameBytes {
		return nil, protoErr(ErrorFrameTooLarge, fmt.Sprintf("%d > %d", len(line), MaxFrameBytes))
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, protoErr(ErrorInvalidJSON, "empty line")
	}
	if trimmed[0] != '{' {
		return nil, protoErr(ErrorInvalidJSON, "frame must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, protoErr(ErrorInvalidJSON, err.Error())
	}
	if dec.More() {
		return nil, protoErr(ErrorInvalidJSON, "trailing data after JSON object")
	}
	if err := validateRawSeq(raw); err != nil {
		return nil, err
	}
	var f Frame
	if err := json.Unmarshal(trimmed, &f); err != nil {
		return nil, protoErr(ErrorInvalidJSON, err.Error())
	}
	if err := ValidateFrame(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

func validateRawSeq(raw map[string]json.RawMessage) error {
	typeRaw, ok := raw["type"]
	if !ok {
		return nil
	}
	var typ string
	if err := json.Unmarshal(typeRaw, &typ); err != nil || typ != TypeEvent {
		return nil
	}
	seqRaw, ok := raw["seq"]
	if !ok {
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(seqRaw, &num); err != nil {
		return protoErr(ErrorInvalidEvent, "seq must be an integer")
	}
	text := num.String()
	if strings.ContainsAny(text, ".eE") {
		return protoErr(ErrorInvalidEvent, "seq must be an integer")
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return protoErr(ErrorInvalidEvent, "seq must be an integer")
	}
	if n < 1 {
		return protoErr(ErrorInvalidEvent, "seq must be >= 1")
	}
	return nil
}

// DecodeLineString is DecodeLine for string input.
func DecodeLineString(line string) (*Frame, error) {
	return DecodeLine([]byte(line))
}

// EncodeFrame marshals a frame to a single NDJSON line without a trailing newline.
// The input frame is never mutated.
func EncodeFrame(f *Frame) ([]byte, error) {
	if f == nil {
		return nil, protoErr(ErrorInvalidJSON, "nil frame")
	}
	cp := *f
	if cp.SchemaVersion == 0 {
		cp.SchemaVersion = SchemaVersion
	}
	if err := ValidateFrame(&cp); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(&cp)
	if err != nil {
		return nil, protoErr(ErrorInvalidJSON, err.Error())
	}
	if len(raw) > MaxFrameBytes {
		return nil, protoErr(ErrorFrameTooLarge, fmt.Sprintf("%d > %d", len(raw), MaxFrameBytes))
	}
	return raw, nil
}

// WriteFrame writes one NDJSON line including a trailing newline.
func WriteFrame(w io.Writer, f *Frame) error {
	raw, err := EncodeFrame(f)
	if err != nil {
		return err
	}
	line := make([]byte, len(raw)+1)
	copy(line, raw)
	line[len(raw)] = '\n'
	_, err = w.Write(line)
	return err
}
