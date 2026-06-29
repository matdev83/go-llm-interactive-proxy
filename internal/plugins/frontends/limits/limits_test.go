package limits

import (
	"testing"
)

func TestStringBytes_UnderLimit(t *testing.T) {
	t.Parallel()
	if err := StringBytes("field1", "hello", 10); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestStringBytes_ExactLimit(t *testing.T) {
	t.Parallel()
	if err := StringBytes("field1", "hello", 5); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestStringBytes_OverLimit(t *testing.T) {
	t.Parallel()
	err := StringBytes("field1", "hello world", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	expected := "field1 has 11 bytes; maximum is 5"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}
