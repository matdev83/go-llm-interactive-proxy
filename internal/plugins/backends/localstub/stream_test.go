package localstub

import (
	"testing"
)

func BenchmarkEventStreamForConfig(b *testing.B) {
	cfg := Config{
		Text:                      "hello world",
		StreamErrorAfterTextDelta: true,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = eventStreamForConfig(cfg)
	}
}
