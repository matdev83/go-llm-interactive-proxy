package billingstore

import "testing"

func TestSQLPlaceholders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		n    int
		want string
	}{
		{name: "empty", n: 0, want: ""},
		{name: "one", n: 1, want: "?"},
		{name: "three", n: 3, want: "?,?,?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sqlPlaceholders(tc.n); got != tc.want {
				t.Fatalf("sqlPlaceholders(%d)=%q want %q", tc.n, got, tc.want)
			}
		})
	}
}
