package stream

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestFlushSSEDataJSON_roundTrip(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	var fr struct {
		Hello string `json:"hello"`
	}
	fr.Hello = "world"
	if err := FlushSSEDataJSON(rec, rec, fr); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !bytes.Contains([]byte(s), []byte(`data: {"hello":"world"}`)) {
		t.Fatalf("body %q", s)
	}
}

func TestFlushSSEEventJSON_roundTrip(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	payload := map[string]string{"k": "v"}
	if err := FlushSSEEventJSON(rec, rec, "evt", payload); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !bytes.Contains([]byte(s), []byte("event: evt")) || !bytes.Contains([]byte(s), []byte(`"k":"v"`)) {
		t.Fatalf("body %q", s)
	}
}

func TestFlushSSEDataJSON_decode(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	type row struct {
		N int `json:"n"`
	}
	if err := FlushSSEDataJSON(rec, rec, row{N: 42}); err != nil {
		t.Fatal(err)
	}
	// Encoder appends newline after JSON object
	lines := bytes.Split(rec.Body.Bytes(), []byte("\n"))
	var dataJSON []byte
	for _, ln := range lines {
		if bytes.HasPrefix(ln, []byte("data: ")) {
			dataJSON = bytes.TrimPrefix(ln, []byte("data: "))
			break
		}
	}
	var got row
	if err := json.Unmarshal(dataJSON, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", dataJSON, err)
	}
	if got.N != 42 {
		t.Fatalf("got %+v", got)
	}
}

func TestFlushSSEDataJoined_roundTrip(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	prefix := []byte(`{"a":`)
	mid := []byte(`"z"`)
	suffix := []byte(`,"b":2}`)
	if err := FlushSSEDataJoined(rec, rec, prefix, mid, suffix); err != nil {
		t.Fatal(err)
	}
	raw := rec.Body.Bytes()
	i := bytes.Index(raw, []byte("data: "))
	if i < 0 {
		t.Fatalf("missing data: %q", raw)
	}
	line := bytes.TrimPrefix(raw[i:], []byte("data: "))
	line = bytes.TrimSpace(line)
	var v map[string]any
	if err := json.Unmarshal(line, &v); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if v["a"] != "z" {
		t.Fatalf("a: %v", v["a"])
	}
}
