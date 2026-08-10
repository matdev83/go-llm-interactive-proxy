package providerprofiles

import (
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type Certification struct {
	ProfileID    string
	Family       Family
	FactoryKind  string
	Capabilities []lipapi.Capability
	Dialects     lipapi.DialectSupport
	Quirks       []QuirkID
}

func Certify(p Profile) (Certification, error) {
	compiled, err := CompileProfile(p)
	if err != nil {
		return Certification{}, err
	}
	caps := make([]lipapi.Capability, 0, len(compiled.Capabilities))
	for cap := range compiled.Capabilities {
		caps = append(caps, cap)
	}
	slices.Sort(caps)
	return Certification{ProfileID: p.ID, Family: p.Family, FactoryKind: compiled.Binding.FactoryKind, Capabilities: caps, Dialects: compiled.Dialects, Quirks: slices.Clone(p.Quirks)}, nil
}

func (c Certification) Validate() error {
	if c.ProfileID == "" || c.FactoryKind == "" || c.Family == "" {
		return fmt.Errorf("incomplete profile certification")
	}
	return nil
}
