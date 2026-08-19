package compactioncontinuity

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/augmentation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

func TestPlaintextAugmentationBoundary_hasNoVerifiedCanonicalCarrier(t *testing.T) {
	t.Parallel()
	if got := augmentation.Capabilities(); len(got) != 0 {
		t.Fatalf("canonical compaction capability allowlist is not empty: %+v", got)
	}

	tests := []struct {
		name  string
		event lipapi.Event
	}{
		{
			name: "encrypted and opaque compaction item",
			event: lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{
				Kind: lipapi.ItemKindCompaction,
				Compaction: &lipapi.CompactionItem{
					EncryptedContent: "ciphertext",
					Opaque:           json.RawMessage(`{"native":"opaque"}`),
				},
			}},
		},
		{
			name:  "event opaque reasoning",
			event: lipapi.Event{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"provider":"opaque"}`)},
		},
		{
			name:  "event signature",
			event: lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: "signature"},
		},
		{
			name: "unknown extension blob",
			event: lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{
				Kind: lipapi.ItemKindExtension,
				Extension: &lipapi.OpaqueExtension{
					Namespace: "unknown",
					Type:      "native-compaction",
					Data:      json.RawMessage(`{"native":"extension"}`),
				},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := augmentation.Match(&tt.event); ok {
				t.Fatal("unverified canonical event matched as mutable plaintext")
			}
		})
	}
}

func TestPlaintextAugmentationBoundary_preservesOpaqueBytesAndQueuesReinjection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "feature-off", true: "feature-on"}[enabled], func(t *testing.T) {
			plugin, parent, background := openFixture(t)
			if !enabled {
				cfg := plugin.cfg
				cfg.Extractor.Enabled = false
				var err error
				plugin, err = New(cfg, parent)
				if err != nil {
					t.Fatal(err)
				}
			}
			seedPlaintextBoundaryState(t, parent)

			for _, fixture := range []struct {
				name  string
				event lipapi.Event
			}{
				{
					name: "compaction encrypted and opaque",
					event: lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{
						Kind: lipapi.ItemKindCompaction,
						Compaction: &lipapi.CompactionItem{
							EncryptedContent: "ciphertext",
							Opaque:           json.RawMessage(`{"compaction":"opaque"}`),
						},
					}},
				},
				{
					name:  "event opaque",
					event: lipapi.Event{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"event":"opaque"}`)},
				},
				{
					name:  "event signature",
					event: lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: "signature"},
				},
				{
					name: "unknown extension",
					event: lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{
						Kind: lipapi.ItemKindExtension,
						Extension: &lipapi.OpaqueExtension{
							Namespace: "unknown",
							Type:      "native-compaction",
							Data:      json.RawMessage(`{"extension":"opaque"}`),
						},
					}},
				},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					event := fixture.event
					before := snapshotBoundaryBytes(event)
					if err := plugin.BeforeResponseRelease(context.Background(), &event,
						compaction.ResponsePreview{Kind: compaction.PreviewCompletionCandidate, TransactionID: "boundary-1"},
						openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
						t.Fatal(err)
					}
					if after := snapshotBoundaryBytes(event); !before.equal(after) {
						t.Fatalf("response-side preservation changed opaque/encrypted/signature bytes: before=%+v after=%+v", before, after)
					}
					if enabled {
						if parent.state.PendingInjection == nil || parent.state.PendingInjection.BoundaryKey != "boundary-1" || parent.state.PendingInjection.CapsuleRevision != 1 {
							t.Fatalf("unsupported carrier did not queue reinjection: %+v", parent.state.PendingInjection)
						}
					} else if parent.state.PendingInjection != nil {
						t.Fatalf("feature-off response created preservation state: %+v", parent.state.PendingInjection)
					}
				})
			}
		})
	}
}

func TestPlaintextAugmentationBoundary_reusesSameRevisionAtDistinctBoundaries(t *testing.T) {
	plugin, parent, background := openFixture(t)
	seedPlaintextBoundaryState(t, parent)
	for i, boundary := range []string{"boundary-1", "boundary-2"} {
		event := lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{
			Kind:       lipapi.ItemKindCompaction,
			Compaction: &lipapi.CompactionItem{EncryptedContent: "ciphertext", Opaque: json.RawMessage(`{"native":true}`)},
		}}
		if err := plugin.BeforeResponseRelease(context.Background(), &event,
			compaction.ResponsePreview{Kind: compaction.PreviewCompletionCandidate, TransactionID: boundary},
			openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
			t.Fatal(err)
		}
		if parent.state.PendingInjection == nil || parent.state.PendingInjection.BoundaryKey != boundary || parent.state.PendingInjection.CapsuleRevision != 1 {
			t.Fatalf("boundary %d pending target=%+v", i+1, parent.state.PendingInjection)
		}

		call := openCall()
		if err := plugin.BeforeRequest(context.Background(), &call,
			compaction.RequestPreview{Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: boundary},
			openMeta(), compaction.Services{}); err != nil {
			t.Fatal(err)
		}
		meta := openMeta()
		meta.TransactionID = boundary
		if err := plugin.AfterResponseRelease(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished}, meta, compaction.Services{}); err != nil {
			t.Fatal(err)
		}
		if parent.state.Revision != 1 || parent.state.PendingInjection != nil || parent.state.LastReleasedInjection == nil || parent.state.LastReleasedInjection.BoundaryKey != boundary {
			t.Fatalf("boundary %d did not release same revision safely: state=%+v", i+1, parent.state)
		}
	}
}

func seedPlaintextBoundaryState(t *testing.T, parent *openParentFake) {
	t.Helper()
	base, err := capsule.New(parent.branch.Binding)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := capsule.Encode(base)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := digestArray(base.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	parent.state = ParentState{Revision: base.Revision, CapsuleJSON: encoded, CapsuleDigest: digest}
}

type boundarySnapshot struct {
	encrypted        string
	compactionOpaque []byte
	eventOpaque      []byte
	extensionData    []byte
	signature        string
}

func snapshotBoundaryBytes(event lipapi.Event) boundarySnapshot {
	out := boundarySnapshot{
		eventOpaque: append([]byte(nil), event.Opaque...),
		signature:   event.Signature,
	}
	if event.Item == nil {
		return out
	}
	if event.Item.Compaction != nil {
		out.encrypted = event.Item.Compaction.EncryptedContent
		out.compactionOpaque = append([]byte(nil), event.Item.Compaction.Opaque...)
	}
	if event.Item.Extension != nil {
		out.extensionData = append([]byte(nil), event.Item.Extension.Data...)
	}
	return out
}

func (before boundarySnapshot) equal(after boundarySnapshot) bool {
	return before.encrypted == after.encrypted &&
		bytes.Equal(before.compactionOpaque, after.compactionOpaque) &&
		bytes.Equal(before.eventOpaque, after.eventOpaque) &&
		bytes.Equal(before.extensionData, after.extensionData) &&
		before.signature == after.signature
}
