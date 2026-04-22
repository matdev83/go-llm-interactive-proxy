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
	for _, base := range []string{"http://127.0.0.1:9", "http://localhost:8080", "http://[::1]:1234"} {
		err := validateBedrockEndpointSecurity(Config{
			DisableHTTPS: true,
			BaseEndpoint: base,
		})
		if err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
	}
}
