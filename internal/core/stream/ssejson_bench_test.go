package stream

import (
	"net/http/httptest"
	"testing"
)

type tinyPayload struct {
	N int `json:"n"`
}

func BenchmarkFlushSSEDataJSON(b *testing.B) {
	rec := httptest.NewRecorder()
	p := tinyPayload{N: 7}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec.Body.Reset()
		if err := FlushSSEDataJSON(rec, rec, p); err != nil {
			b.Fatal(err)
		}
	}
}
