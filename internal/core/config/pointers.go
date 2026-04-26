package config

func BoolPtr(v bool) *bool {
	p := v
	return &p
}
