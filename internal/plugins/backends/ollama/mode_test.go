package ollama

import "testing"

func TestDiscoveryLocalForMode_strictSplit(t *testing.T) {
	t.Parallel()

	trueVal := true
	falseVal := false
	disabled := DiscoveryConfig{Enabled: &falseVal}

	cases := []struct {
		name string
		mode backendMode
		d    DiscoveryConfig
		want bool
	}{
		{
			name: "local mode default",
			mode: backendModeLocal,
			d:    DiscoveryConfig{Enabled: &trueVal},
			want: true,
		},
		{
			name: "local mode explicit local false",
			mode: backendModeLocal,
			d:    DiscoveryConfig{Enabled: &trueVal, Local: &falseVal},
			want: false,
		},
		{
			name: "local mode ignores cloud true",
			mode: backendModeLocal,
			d:    DiscoveryConfig{Enabled: &trueVal, Cloud: &trueVal},
			want: true,
		},
		{
			name: "cloud mode never local",
			mode: backendModeCloud,
			d:    DiscoveryConfig{Enabled: &trueVal, Local: &trueVal},
			want: false,
		},
		{
			name: "global disabled",
			mode: backendModeLocal,
			d:    disabled,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := discoveryLocalForMode(tc.mode, tc.d); got != tc.want {
				t.Fatalf("discoveryLocalForMode() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiscoveryCloudForMode_strictSplit(t *testing.T) {
	t.Parallel()

	trueVal := true
	falseVal := false
	disabled := DiscoveryConfig{Enabled: &falseVal}

	cases := []struct {
		name string
		mode backendMode
		d    DiscoveryConfig
		want bool
	}{
		{
			name: "cloud mode default",
			mode: backendModeCloud,
			d:    DiscoveryConfig{Enabled: &trueVal},
			want: true,
		},
		{
			name: "cloud mode explicit cloud false",
			mode: backendModeCloud,
			d:    DiscoveryConfig{Enabled: &trueVal, Cloud: &falseVal},
			want: false,
		},
		{
			name: "cloud mode ignores local true",
			mode: backendModeCloud,
			d:    DiscoveryConfig{Enabled: &trueVal, Local: &trueVal},
			want: true,
		},
		{
			name: "local mode never cloud",
			mode: backendModeLocal,
			d:    DiscoveryConfig{Enabled: &trueVal, Cloud: &trueVal},
			want: false,
		},
		{
			name: "global disabled",
			mode: backendModeCloud,
			d:    disabled,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := discoveryCloudForMode(tc.mode, tc.d); got != tc.want {
				t.Fatalf("discoveryCloudForMode() = %v, want %v", got, tc.want)
			}
		})
	}
}
