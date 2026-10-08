package secretguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestGuard_JSONBackingBytesRemainUnchangedAcrossDecisions(t *testing.T) {
	shared := json.RawMessage(` {"value": "credential"} `)
	original := bytes.Clone(shared)
	for _, action := range []string{ActionBlock, ActionLog, ActionRedact} {
		for _, failure := range []string{"none", "limit", "matcher"} {
			if action != ActionRedact && failure != "none" {
				continue
			}
			t.Run(action+"/"+failure, func(t *testing.T) {
				call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartJSON, Content: shared}, lipapi.TextPart("tail")}}}}
				before := lipapi.CloneCall(call)
				cfg := Config{Action: action}
				var matcher sdk.Matcher = newExactStub("credential", "SECRET", sdk.SourceCategoryProxyEnv)
				switch failure {
				case "limit":
					cfg.ScanMaxBytes = len(shared)
				case "matcher":
					matcher = &failingRedactMatcher{match: "tail", err: errors.New("fixture error")}
				}
				d, err := NewGuard(cfg).Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(matcher))
				if !bytes.Equal(shared, original) {
					t.Fatal("guard changed a backing buffer shared with another request")
				}
				if failure == "matcher" && err == nil || failure != "matcher" && (err != nil || d.Validate() != nil) {
					t.Fatal("guard lost its decision/error contract")
				}
				if action != ActionRedact || failure != "none" {
					if !reflect.DeepEqual(call, before) {
						t.Fatal("read-only or unsuccessful redaction changed the canonical call")
					}
				} else if d.Outcome != sdk.OutcomeRedacted || bytes.Contains(call.Messages[0].Parts[0].Content, []byte("credential")) {
					t.Fatal("successful redaction did not publish masked JSON")
				}
			})
		}
	}
}
