package bedrock

import "testing"

func TestValidateBedrockEndpointSecurity_rejectsNonLoopbackWithoutFlag(t *testing.T) {
	t.Parallel()
	err := validateBedrockEndpointSecurity(Config{
		DisableHTTPS: true,
		BaseEndpoint: "http://example.com",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateBedrockEndpointSecurity_allowsNonLoopbackWithFlag(t *testing.T) {
	t.Parallel()
	err := validateBedrockEndpointSecurity(Config{
		DisableHTTPS:             true,
		BaseEndpoint:             "http://example.com",
		AllowInsecureNonLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateBedrockEndpointSecurity_allowsLoopbackHTTP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		base string
	}{
		{"ipv4_loopback", "http://127.0.0.1:9"},
		{"localhost", "http://localhost:8080"},
		{"ipv6_loopback", "http://[::1]:1234"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateBedrockEndpointSecurity(Config{
				DisableHTTPS: true,
				BaseEndpoint: tc.base,
			})
			if err != nil {
				t.Fatalf("base %q: %v", tc.base, err)
			}
		})
	}
}
