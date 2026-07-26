package runtimebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"gopkg.in/yaml.v3"
)

// BackendStateIdentity is the canonical key for affinity/health observation reuse
// across request-plane candidates (requirements 6.7–6.8).
type BackendStateIdentity struct {
	InstanceID   string
	FactoryKind  string
	ConfigDigest string
}

// Compatible reports whether two identities may safely share affinity/health state.
func (k BackendStateIdentity) Compatible(other BackendStateIdentity) bool {
	return k.InstanceID == other.InstanceID &&
		k.FactoryKind == other.FactoryKind &&
		k.ConfigDigest == other.ConfigDigest &&
		k.InstanceID != ""
}

// Namespace returns a stable opaque prefix for keyed observation storage.
func (k BackendStateIdentity) Namespace() string {
	if k.InstanceID == "" {
		return ""
	}
	return k.FactoryKind + "/" + k.InstanceID + "@" + k.ConfigDigest
}

// BackendStateIdentityFromPlugin derives a process state identity from one backend row.
func BackendStateIdentityFromPlugin(p config.PluginConfig) (BackendStateIdentity, error) {
	id := p.InstanceID()
	kind := p.FactoryID()
	if id == "" {
		return BackendStateIdentity{}, fmt.Errorf("runtimebundle: empty backend instance id")
	}
	configRaw, err := marshalPluginConfigNode(p.Config)
	if err != nil {
		return BackendStateIdentity{}, fmt.Errorf("runtimebundle: backend identity marshal: %w", err)
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "kind=%s\nid=%s\nenabled=%v\n", kind, id, p.Enabled)
	_, _ = h.Write(configRaw)
	sum := h.Sum(nil)
	return BackendStateIdentity{
		InstanceID:   id,
		FactoryKind:  kind,
		ConfigDigest: hex.EncodeToString(sum[:16]),
	}, nil
}

func marshalPluginConfigNode(n yaml.Node) ([]byte, error) {
	if n.Kind == 0 && n.Tag == "" && n.Value == "" && len(n.Content) == 0 {
		return nil, nil
	}
	node := n
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, nil
		}
		node = *node.Content[0]
	}
	return yaml.Marshal(&node)
}

// BackendStateIdentitiesFromConfig maps instance id → identity for enabled and
// disabled backend rows present in cfg (absent ids are simply omitted).
func BackendStateIdentitiesFromConfig(cfg *config.Config) (map[string]BackendStateIdentity, error) {
	out := make(map[string]BackendStateIdentity)
	if cfg == nil {
		return out, nil
	}
	for _, p := range cfg.Plugins.Backends {
		id := p.InstanceID()
		if id == "" {
			continue
		}
		key, err := BackendStateIdentityFromPlugin(p)
		if err != nil {
			return nil, err
		}
		out[id] = key
	}
	return out, nil
}

// StoreCompatKey identifies a process-owned store/state namespace (req 6.6–6.7).
type StoreCompatKey struct {
	Kind   string
	Digest string
}

// StoreCompatKeyFromContinuity derives continuity store topology identity.
func StoreCompatKeyFromContinuity(c config.ContinuityConfig) StoreCompatKey {
	parts := []string{
		"continuity",
		fmt.Sprintf("in_memory=%v", c.InMemory),
		"store=" + strings.TrimSpace(c.Store),
		"sqlite=" + strings.TrimSpace(c.SQLitePath),
		"postgres_dsn_set=" + fmt.Sprint(strings.TrimSpace(c.PostgresDSN) != ""),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return StoreCompatKey{Kind: "continuity", Digest: hex.EncodeToString(sum[:16])}
}

// StoreCompatKeyFromSecureSession derives secure-session store topology identity.
func StoreCompatKeyFromSecureSession(s config.SecureSessionConfig) StoreCompatKey {
	parts := []string{
		"secure_session",
		"store=" + strings.TrimSpace(s.Store),
		"sqlite=" + strings.TrimSpace(s.SQLitePath),
		"postgres_dsn_set=" + fmt.Sprint(strings.TrimSpace(s.PostgresDSN) != ""),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return StoreCompatKey{Kind: "secure_session", Digest: hex.EncodeToString(sum[:16])}
}

// StoreCompatKeyFromAccountingLedger derives accounting ledger topology identity.
func StoreCompatKeyFromAccountingLedger(a config.AccountingConfig) StoreCompatKey {
	parts := []string{
		"accounting_ledger",
		fmt.Sprintf("enabled=%v", a.Enabled),
		"store=" + strings.TrimSpace(a.Ledger.Store),
		"sqlite=" + strings.TrimSpace(a.Ledger.SQLitePath),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return StoreCompatKey{Kind: "accounting_ledger", Digest: hex.EncodeToString(sum[:16])}
}

// StoreCompatKeyFromMeteringJournal derives metering journal topology identity.
func StoreCompatKeyFromMeteringJournal(m config.MeteringConfig) StoreCompatKey {
	parts := []string{
		"metering_journal",
		fmt.Sprintf("enabled=%v", m.Enabled),
		"store=" + strings.TrimSpace(m.Journal.Store),
		"sqlite=" + strings.TrimSpace(m.Journal.SQLitePath),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return StoreCompatKey{Kind: "metering_journal", Digest: hex.EncodeToString(sum[:16])}
}
