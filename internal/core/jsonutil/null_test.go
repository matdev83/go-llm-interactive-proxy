package jsonutil

import "testing"

func TestIsJSONNull(t *testing.T) {
	if !IsJSONNull([]byte("null")) {
		t.Fatal("null literal")
	}
	if IsJSONNull(nil) || IsJSONNull([]byte{}) || IsJSONNull([]byte(" null ")) || IsJSONNull([]byte(`"null"`)) {
		t.Fatal("non-exact null")
	}
}

func TestIsAbsentOrJSONNull(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, []byte("null")} {
		if !IsAbsentOrJSONNull(raw) {
			t.Fatalf("want absent/null: %q", raw)
		}
	}
	if IsAbsentOrJSONNull([]byte("{}")) {
		t.Fatal("object is not null")
	}
}

func TestIsPresentNonNullJSON(t *testing.T) {
	if !IsPresentNonNullJSON([]byte("{}")) {
		t.Fatal("{}")
	}
	for _, raw := range [][]byte{nil, {}, []byte("null")} {
		if IsPresentNonNullJSON(raw) {
			t.Fatalf("want false: %q", raw)
		}
	}
}
