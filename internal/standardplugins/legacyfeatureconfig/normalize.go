package legacyfeatureconfig

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// CanonicalID is the feature identifier in plugins.features.
const CanonicalID = "interleaved-thinking"

// NormalizeYAML takes raw YAML configuration bytes and normalizes any legacy
// top-level 'interleaved:' configuration into a canonical 'plugins.features' entry.
// If both legacy and canonical entries exist, a deterministic conflict error is returned.
func NormalizeYAML(raw []byte) ([]byte, error) {
	if !bytes.Contains(raw, []byte("interleaved")) {
		return raw, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		return raw, nil
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return raw, nil
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	hasInterleaved := false
	if doc.Kind == yaml.MappingNode {
		for i := 0; i < len(doc.Content); i += 2 {
			if doc.Content[i].Value == "interleaved" {
				hasInterleaved = true
				break
			}
		}
	}
	if !hasInterleaved {
		return raw, nil
	}
	if err := NormalizeNode(&root); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("legacyfeatureconfig: encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("legacyfeatureconfig: close encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// NormalizeNode mutates the given YAML root AST in-place, converting any legacy
// top-level 'interleaved:' mapping into a canonical 'plugins.features' entry with
// id 'interleaved-thinking'. If both are present, a conflict error is returned.
func NormalizeNode(root *yaml.Node) error {
	if root == nil {
		return nil
	}
	doc := root
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}

	interleavedIdx := -1
	for i := 0; i < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "interleaved" {
			interleavedIdx = i
			break
		}
	}

	pluginsIdx := -1
	var pluginsNode *yaml.Node
	var featuresNode *yaml.Node
	hasCanonicalInterleaved := false

	for i := 0; i < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "plugins" {
			pluginsIdx = i
			pluginsNode = doc.Content[i+1]
			break
		}
	}

	if pluginsNode != nil && pluginsNode.Kind == yaml.MappingNode {
		for i := 0; i < len(pluginsNode.Content); i += 2 {
			if pluginsNode.Content[i].Value == "features" {
				featuresNode = pluginsNode.Content[i+1]
				break
			}
		}
	}

	if featuresNode != nil && featuresNode.Kind == yaml.SequenceNode {
		for _, item := range featuresNode.Content {
			if item.Kind == yaml.MappingNode {
				for j := 0; j < len(item.Content); j += 2 {
					if item.Content[j].Value == "id" && item.Content[j+1].Value == CanonicalID {
						hasCanonicalInterleaved = true
						break
					}
				}
			}
		}
	}

	if interleavedIdx >= 0 {
		interleavedVal := doc.Content[interleavedIdx+1]
		if isNullNode(interleavedVal) {
			// Null legacy node -> NO feature entry synthesized (absent stays absent; do not default enabled=true)
			doc.Content = append(doc.Content[:interleavedIdx], doc.Content[interleavedIdx+2:]...)
			return nil
		}
		if interleavedVal.Kind != yaml.MappingNode {
			tag := interleavedVal.Tag
			if tag == "" {
				switch interleavedVal.Kind {
				case yaml.ScalarNode:
					tag = "scalar"
				case yaml.SequenceNode:
					tag = "sequence"
				default:
					tag = fmt.Sprintf("kind %d", interleavedVal.Kind)
				}
			}
			return fmt.Errorf("legacyfeatureconfig: legacy top-level 'interleaved' must be a mapping, got %s", tag)
		}
	}

	if interleavedIdx >= 0 && hasCanonicalInterleaved {
		return fmt.Errorf("legacyfeatureconfig: both legacy top-level 'interleaved' and canonical 'plugins.features' entry for %q are configured", CanonicalID)
	}

	if interleavedIdx < 0 {
		return nil
	}

	interleavedVal := doc.Content[interleavedIdx+1]

	enabledVal := "true"
	for i := 0; i < len(interleavedVal.Content); i += 2 {
		if interleavedVal.Content[i].Value == "enabled" {
			enabledVal = interleavedVal.Content[i+1].Value
			break
		}
	}

	canonicalEntry := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: CanonicalID},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enabled"},
			{Kind: yaml.ScalarNode, Tag: "!!bool", Value: enabledVal},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "config"},
			interleavedVal,
		},
	}

	if pluginsNode == nil {
		pluginsNode = &yaml.Node{
			Kind:    yaml.MappingNode,
			Tag:     "!!map",
			Content: []*yaml.Node{},
		}
		doc.Content = append(doc.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "plugins"},
			pluginsNode,
		)
		_ = pluginsIdx
	}

	if featuresNode == nil {
		featuresNode = &yaml.Node{
			Kind:    yaml.SequenceNode,
			Tag:     "!!seq",
			Content: []*yaml.Node{},
		}
		pluginsNode.Content = append(pluginsNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "features"},
			featuresNode,
		)
	}

	featuresNode.Content = append(featuresNode.Content, canonicalEntry)

	// Remove legacy 'interleaved' key-value pair from top-level doc.Content
	doc.Content = append(doc.Content[:interleavedIdx], doc.Content[interleavedIdx+2:]...)
	return nil
}

func isNullNode(node *yaml.Node) bool {
	if node == nil {
		return true
	}
	if node.Tag == "!!null" {
		return true
	}
	if node.Kind == yaml.ScalarNode && node.Tag != "!!str" && (node.Value == "null" || node.Value == "~" || node.Value == "") {
		return true
	}
	return false
}
