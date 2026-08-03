package openresponses

import proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"

func effectiveProtocolLimits(limits proto.Limits) proto.Limits {
	if limits == (proto.Limits{}) {
		return proto.DefaultLimits()
	}
	return limits
}
