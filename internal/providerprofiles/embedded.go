package providerprofiles

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed catalog.json
var embeddedCatalog []byte

// EmbeddedCatalog loads the checked-in catalog without network or process work.
func EmbeddedCatalog() (*Catalog, error) {
	var profiles []Profile
	if err := json.Unmarshal(embeddedCatalog, &profiles); err != nil {
		return nil, fmt.Errorf("provider profiles: decode embedded catalog: %w", err)
	}
	return NewCatalog(profiles)
}

func EmbeddedProfile(id string) (Profile, error) {
	catalog, err := EmbeddedCatalog()
	if err != nil {
		return Profile{}, err
	}
	for _, profile := range catalog.Profiles() {
		if profile.ID == id {
			return profile, nil
		}
	}
	return Profile{}, fmt.Errorf("unknown embedded provider profile %q", id)
}
