package toolcallrepair

func ExportObjectFields(v any) (keys []string, values map[string]any, ok bool) {
	return objectFields(v)
}
