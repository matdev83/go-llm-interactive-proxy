package openaicodex

// isStrictCompatibleSchema reports whether a JSON schema satisfies the Codex
// Responses API "strict" tool-schema requirements: every object must declare
// additionalProperties:false and list all of its properties in required. Schemas
// that do not comply must be sent with strict:false, otherwise the upstream
// rejects the request (e.g. "additionalProperties is required to be supplied
// and to be false"). Parameterless schemas (no properties) are treated as
// compatible so they keep strict:true. The check is conservative: when in
// doubt it returns false, which only relaxes strict mode (safe) and never
// causes an upstream rejection.
func isStrictCompatibleSchema(schema map[string]any) bool {
	if hasRef(schema) || !strictCompatibleCompositions(schema) {
		return false
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return strictCompatibleArrayItems(schema)
	}
	ap, ok := schema["additionalProperties"]
	if !ok {
		return false
	}
	if asBool, ok := ap.(bool); !ok || asBool {
		return false
	}
	reqRaw, _ := schema["required"].([]any)
	required := make(map[string]bool, len(reqRaw))
	for _, r := range reqRaw {
		if s, ok := r.(string); ok {
			required[s] = true
		}
	}
	for name, prop := range props {
		if !required[name] {
			return false
		}
		child, ok := prop.(map[string]any)
		if !ok {
			return false
		}
		if !isStrictCompatibleChild(child) {
			return false
		}
	}
	return strictCompatibleArrayItems(schema)
}

func normalizeToolSchemaForCodex(schema map[string]any) (map[string]any, bool) {
	addStrictAdditionalProperties(schema)
	return schema, isStrictCompatibleSchema(schema)
}

func addStrictAdditionalProperties(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			addStrictAdditionalProperties(child)
			x[k] = child
		}
		if props, ok := x["properties"].(map[string]any); ok && len(props) > 0 {
			if _, ok := x["additionalProperties"]; !ok {
				x["additionalProperties"] = false
			}
		}
	case []any:
		for i, child := range x {
			addStrictAdditionalProperties(child)
			x[i] = child
		}
	}
}

func isStrictCompatibleChild(node map[string]any) bool {
	if hasRef(node) || !strictCompatibleCompositions(node) {
		return false
	}
	if props, ok := node["properties"].(map[string]any); ok && len(props) > 0 {
		return isStrictCompatibleSchema(node)
	}
	switch t, _ := node["type"].(string); t {
	case "object":
		return isStrictCompatibleSchema(node)
	case "array":
		return strictCompatibleArrayItems(node)
	}
	return true
}

func hasRef(node map[string]any) bool {
	_, ok := node["$ref"]
	return ok
}

func strictCompatibleCompositions(node map[string]any) bool {
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		raw, ok := node[key]
		if !ok {
			continue
		}
		items, ok := raw.([]any)
		if !ok || len(items) == 0 {
			return false
		}
		for _, item := range items {
			child, ok := item.(map[string]any)
			if !ok || !isStrictCompatibleChild(child) {
				return false
			}
		}
	}
	return true
}

func strictCompatibleArrayItems(node map[string]any) bool {
	raw, ok := node["items"]
	if !ok {
		return true
	}
	if child, ok := raw.(map[string]any); ok {
		return isStrictCompatibleChild(child)
	}
	items, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		child, ok := item.(map[string]any)
		if !ok || !isStrictCompatibleChild(child) {
			return false
		}
	}
	return true
}
