package toolcallrepair

func NormalizeASCIIName(name string) string {
	if name == "" {
		return ""
	}
	out := make([]byte, 0, len(name))
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' || c == '_' || c == '-':
			continue
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
