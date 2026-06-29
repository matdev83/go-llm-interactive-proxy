package limits

import (
	"testing"
)

func TestCount(t *testing.T) {
	if err := Count("test", 5, 10); err != nil {
		t.Errorf("expected no error for got < max, got: %v", err)
	}
	if err := Count("test", 10, 10); err != nil {
		t.Errorf("expected no error for got == max, got: %v", err)
	}
	if err := Count("test", 11, 10); err == nil {
		t.Error("expected error for got > max, got nil")
	}
}
