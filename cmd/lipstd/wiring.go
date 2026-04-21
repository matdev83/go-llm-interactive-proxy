package main

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"

// mandatoryStandardPlugins lists plugins that must appear in loaded configuration for the
// reference distribution composition root (IDs must align with config/config.yaml).
func mandatoryStandardPlugins() []lipsdk.Requirement {
	return lipsdk.StandardDistributionRequirements()
}
