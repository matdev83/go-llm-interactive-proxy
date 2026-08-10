package contract

import (
	"encoding/json"
	"testing"
)

func TestBaselineScenarioCorpusJSONRoundTrip(t *testing.T) {
	corpus := BaselineScenarioCorpus()
	encoded, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []ScenarioDescriptor
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(corpus) {
		t.Fatalf("decoded=%d want %d", len(decoded), len(corpus))
	}
	for i := range corpus {
		if decoded[i].ID != corpus[i].ID || decoded[i].Feature != corpus[i].Feature || decoded[i].Transport != corpus[i].Transport {
			t.Fatalf("scenario %d changed: %#v != %#v", i, decoded[i], corpus[i])
		}
	}
}

func TestBaselineScenarioCorpusSchema(t *testing.T) {
	for _, scenario := range BaselineScenarioCorpus() {
		if scenario.ID == "" || scenario.Feature == "" || scenario.Transport == "" {
			t.Fatalf("incomplete scenario: %#v", scenario)
		}
	}
}
