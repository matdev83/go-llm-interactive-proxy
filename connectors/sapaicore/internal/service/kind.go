package service

import "time"

const (
	FactoryKind = "sapaicore"
	PluginID    = "io.golip.backend.sapaicore"
	DisplayName = "SAP AI Core"
	Description = "SAP AI Core REST connector"

	DefaultHTTPTimeout          = 60 * time.Second
	InferenceContractOpenAIChat = "openai-chat"
)
