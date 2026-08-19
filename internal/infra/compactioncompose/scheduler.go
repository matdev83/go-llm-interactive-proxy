package compactioncompose

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// NewProductionBackgroundScheduler creates the process-lifetime bounded pool.
// Bounds come from the initial configuration and remain immutable across reload.
func NewProductionBackgroundScheduler(ctx context.Context, cfg *config.Config) *auxreq.BackgroundScheduler {
	schedulerCfg := auxreq.SchedulerConfig{
		Workers: featurecontinuity.DefaultMaxConcurrency, QueueCapacity: featurecontinuity.DefaultQueueCapacity,
		MaxResults: featurecontinuity.DefaultResultMaxCount, ResultTTL: featurecontinuity.DefaultPendingResultTTL,
		JobTimeout: featurecontinuity.DefaultExtractorTimeout, MaxResultBytes: featurecontinuity.DefaultResultMaxBytes,
	}
	for _, reg := range config.RegistrationsFromConfig(cfg) {
		if reg.Kind != lipsdk.PluginKindFeature || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		featureCfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			continue
		}
		schedulerCfg.Workers, schedulerCfg.QueueCapacity = featureCfg.Worker.MaxConcurrency, featureCfg.Worker.QueueCapacity
		schedulerCfg.MaxResults, schedulerCfg.ResultTTL = featureCfg.Result.MaxCount, featureCfg.Result.TTL
		schedulerCfg.JobTimeout, schedulerCfg.MaxResultBytes = featureCfg.Extractor.Timeout, featureCfg.Result.MaxBytes
		break
	}
	scheduler, err := auxreq.NewBackgroundScheduler(ctx, nil, schedulerCfg)
	if err == nil {
		return scheduler
	}
	scheduler, _ = auxreq.NewBackgroundScheduler(ctx, nil, auxreq.SchedulerConfig{})
	return scheduler
}
