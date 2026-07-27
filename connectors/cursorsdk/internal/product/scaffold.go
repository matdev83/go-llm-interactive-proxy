package product

import (
	"log/slog"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type Scaffold struct {
	cfg        Config
	source     ModelListSource
	starter    ProcessStarter
	hostEnv    []string
	hasEnv     bool
	log        *slog.Logger
	instanceID string
}

func NewScaffold(cfg Config) Scaffold {
	return Scaffold{cfg: cfg}
}

func (s Scaffold) WithModelListSource(source ModelListSource) Scaffold {
	s.source = source
	return s
}

func (s Scaffold) WithProcessStarter(starter ProcessStarter) Scaffold {
	s.starter = starter
	return s
}

func (s Scaffold) WithHostEnv(hostEnv []string) Scaffold {
	s.hostEnv = append([]string(nil), hostEnv...)
	s.hasEnv = true
	return s
}

func (s Scaffold) WithLogger(log *slog.Logger) Scaffold {
	s.log = log
	return s
}

func (s Scaffold) WithInstanceID(id string) Scaffold {
	s.instanceID = strings.TrimSpace(id)
	return s
}

func (s Scaffold) Backend() execbackend.Backend {
	opts := runtimeOpts{
		Starter:         s.starter,
		ModelListSource: s.source,
		Log:             s.log,
		InstanceID:      s.instanceID,
	}
	if s.hasEnv {
		opts.HostEnv = s.hostEnv
	}
	return newBackendRuntime(s.cfg, opts).asBackend()
}

func (s Scaffold) APIKeyEquals(candidate string) bool {
	return s.cfg.APIKey == candidate
}

func (s Scaffold) HasAPIKey() bool {
	return strings.TrimSpace(s.cfg.APIKey) != ""
}

func (s Scaffold) BridgeExecutable() string {
	return s.cfg.BridgeExecutable
}

func (s Scaffold) SandboxMode() SandboxMode {
	return s.cfg.SandboxMode
}

func (s Scaffold) SettingSources() []SettingSource {
	return append([]SettingSource(nil), s.cfg.SettingSources...)
}

func (s Scaffold) MaxAgents() int {
	return s.cfg.MaxAgents
}

func (s Scaffold) MaxConcurrentRuns() int {
	return s.cfg.MaxConcurrentRuns
}

func (s Scaffold) EnvAllowlist() []string {
	return append([]string(nil), s.cfg.BridgeEnvAllowlist...)
}

func (s Scaffold) ConfigSansSecret() Config {
	c := s.cfg
	c.APIKey = ""
	return c
}

var _ modelinventory.Provider = (*inventoryProvider)(nil)
