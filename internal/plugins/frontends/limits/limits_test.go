package limits

import (
	"testing"
)

func TestBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		field   string
		got     int
		max     int
		wantErr bool
		errMsg  string
	}{
		{
			name:    "under limit",
			field:   "payload",
			got:     50,
			max:     100,
			wantErr: false,
		},
		{
			name:    "exact limit",
			field:   "payload",
			got:     100,
			max:     100,
			wantErr: false,
		},
		{
			name:    "over limit",
			field:   "payload",
			got:     150,
			max:     100,
			wantErr: true,
			errMsg:  "payload has 150 bytes; maximum is 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Bytes(tt.field, tt.got, tt.max)
			if (err != nil) != tt.wantErr {
				t.Errorf("Bytes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("Bytes() error message = %q, want %q", err.Error(), tt.errMsg)
			}
		})
	}
}
