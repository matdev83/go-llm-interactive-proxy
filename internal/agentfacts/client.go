package agentfacts

import "strings"

type ClientFamily string

const (
	ClientUnknown  ClientFamily = ""
	ClientCodex    ClientFamily = "codex"
	ClientRoo      ClientFamily = "roo"
	ClientOpenCode ClientFamily = "opencode"
	ClientPi       ClientFamily = "pi"
	ClientDroid    ClientFamily = "droid"
	ClientHermes   ClientFamily = "hermes"
)

type ClientMatch struct {
	Family         ClientFamily
	HighConfidence bool
}

func MatchClientIdentity(raw string) ClientMatch {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return ClientMatch{}
	}
	// Stable identity families only. Deliberately avoid generic SDK UAs and
	// arbitrary substring/fuzzy matching.
	switch {
	case tokenOrPrefix(v, "codex_cli_rs"):
		return ClientMatch{Family: ClientCodex, HighConfidence: true}
	case tokenOrPrefix(v, "roo-code"):
		return ClientMatch{Family: ClientRoo, HighConfidence: true}
	case tokenOrPrefix(v, "opencode"):
		return ClientMatch{Family: ClientOpenCode, HighConfidence: true}
	case tokenOrPrefix(v, "@mariozechner/pi-coding-agent"), tokenOrPrefix(v, "pi-coding-agent"):
		return ClientMatch{Family: ClientPi, HighConfidence: true}
	case tokenOrPrefix(v, "factory-cli"), tokenOrPrefix(v, "factory_cli"), tokenOrPrefix(v, "factorydroid"):
		return ClientMatch{Family: ClientDroid, HighConfidence: true}
	case tokenOrPrefix(v, "hermes-agent"), tokenOrPrefix(v, "nousresearch/hermes-agent"):
		return ClientMatch{Family: ClientHermes, HighConfidence: true}
	default:
		return ClientMatch{}
	}
}

func tokenOrPrefix(v, marker string) bool {
	if v == marker {
		return true
	}
	if strings.HasPrefix(v, marker+"/") || strings.HasPrefix(v, marker+" ") || strings.HasPrefix(v, marker+"(") {
		return true
	}
	for _, tok := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ' ' || r == ';' || r == ',' || r == '(' || r == ')' || r == '[' || r == ']'
	}) {
		if tok == marker || strings.HasPrefix(tok, marker+"/") {
			return true
		}
	}
	return false
}
