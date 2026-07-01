package openaicodex

import "testing"

func TestNormalizeTransport_defaultsAndValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		in           string
		experimental bool
		want         string
	}{
		{"empty defaults to https", "", false, TransportHTTPS},
		{"auto allowed with experimental websocket", "auto", true, TransportAuto},
		{"https is case insensitive", "HTTPS", false, TransportHTTPS},
		{"websocket trims whitespace", "  websocket  ", true, TransportWebSocket},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeTransport(tc.in, tc.experimental)
			if err != nil {
				t.Fatalf("NormalizeTransport(%q) err: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeTransport(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeTransport_invalidErrors(t *testing.T) {
	t.Parallel()
	if _, err := NormalizeTransport("quic", true); err == nil {
		t.Fatal("expected error for unknown transport")
	}
}

func TestNormalizeTransport_webSocketRequiresExperimentalOptIn(t *testing.T) {
	t.Parallel()
	for _, transport := range []string{TransportAuto, TransportWebSocket} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			if _, err := NormalizeTransport(transport, false); err == nil {
				t.Fatalf("expected %q to require experimental websocket opt-in", transport)
			}
		})
	}
}
