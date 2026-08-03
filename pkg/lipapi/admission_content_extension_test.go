package lipapi_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAdmission_OpaqueExtensionContentPartNegotiation(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:part", `{"type":"acme:part"}`)))
	required := lipapi.RequiredCapabilities(call)

	// Without CapabilityOpaqueExtensions the backend must hard-reject before work.
	without := lipapi.Negotiate(required, lipapi.NewBackendCaps(lipapi.CapabilityOrderedItems, lipapi.CapabilityDocuments))
	if without.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected reject without opaque_extensions, got %s", without.Kind)
	}
	// With CapabilityOpaqueExtensions admission is lossless.
	with := lipapi.Negotiate(required, lipapi.NewBackendCaps(lipapi.CapabilityOrderedItems, lipapi.CapabilityOpaqueExtensions))
	if with.Kind != lipapi.NegotiationLossless {
		t.Fatalf("expected lossless with opaque_extensions, got %s", with.Kind)
	}
}

func TestAdmission_FileAndVideoContentPartNegotiation(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{itemWith(
		lipapi.ContentPart{Kind: lipapi.ContentPartFileRef, FileRef: "https://x/report.pdf"},
		lipapi.ContentPart{Kind: lipapi.ContentPartVideoRef, VideoRef: "https://x/v.mp4"},
	)}}
	required := lipapi.RequiredCapabilities(call)
	caps := lipapi.NewBackendCaps()
	for _, c := range required {
		caps[c] = struct{}{}
	}
	if res := lipapi.Negotiate(required, caps); res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("expected lossless with full caps, got %s missing=%v", res.Kind, res.Missing)
	}
	// Dropping video_input must hard-reject (video is not soft-downgradable).
	noVideo := lipapi.NewBackendCaps(lipapi.CapabilityOrderedItems, lipapi.CapabilityDocuments)
	if res := lipapi.Negotiate(required, noVideo); res.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected reject without video_input, got %s", res.Kind)
	}
}

func TestAdmission_InlineFileDataRequiresDocuments(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{itemWith(
		lipapi.ContentPart{Kind: lipapi.ContentPartFileRef, FileData: "aGVsbG8="},
	)}}
	required := lipapi.RequiredCapabilities(call)
	hasDocs := false
	for _, c := range required {
		if c == lipapi.CapabilityDocuments {
			hasDocs = true
		}
	}
	if !hasDocs {
		t.Fatalf("inline file_data call must require documents capability, got %v", required)
	}
	res := lipapi.Negotiate(required, lipapi.NewBackendCaps(lipapi.CapabilityOrderedItems))
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected reject without documents, got %s", res.Kind)
	}
}

func TestJSONRoundTrip_ContentPartExtensionAndFileData(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{itemWith(
		lipapi.ContentPart{Kind: lipapi.ContentPartFileRef, FileData: "aGVsbG8=", FileName: "minimal.pdf"},
		extContentPart("acme:part", `{"type":"acme:part","payload":{"k":1}}`),
	)}}
	raw, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	var back lipapi.Call
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Items[0].Content
	if got[0].FileData != "aGVsbG8=" || got[0].FileName != "minimal.pdf" {
		t.Fatalf("file_data json round-trip mismatch: %+v", got[0])
	}
	if got[1].Kind != lipapi.ContentPartExtension || got[1].Extension == nil ||
		got[1].Extension.Type != "acme:part" || string(got[1].Extension.Data) != `{"type":"acme:part","payload":{"k":1}}` {
		t.Fatalf("extension json round-trip mismatch: %+v", got[1])
	}
}
