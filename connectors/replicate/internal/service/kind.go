package service

import "time"

const (
	FactoryKind = "replicate"
	PluginID    = "io.golip.backend.replicate"
	DisplayName = "Replicate"
	Description = "Replicate prediction lifecycle connector"

	DefaultOrigin       = "https://api.replicate.com"
	DefaultHTTPTimeout  = 60 * time.Second
	DefaultPollInterval = 100 * time.Millisecond
)
