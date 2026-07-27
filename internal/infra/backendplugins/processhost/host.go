package processhost

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"golang.org/x/sync/singleflight"
)

// Config configures a process supervisor.
type Config struct {
	Launcher Launcher
	Channel  ChannelFactory
	WorkDir  string
	AllowEnv []string
}

type instanceState int

const (
	instancePending instanceState = iota
	instanceConfigured
	instanceFailed
)

// Host supervises lazy plugin process activation and peer-gated configuration.
type Host struct {
	cfg Config

	mu         sync.Mutex
	closing    bool
	nextGen    uint64
	slots      map[string]*generationSlot
	instances  map[string]*instanceRec
	launchSF   singleflight.Group
	launchNote func()
}

type generationSlot struct {
	key        string
	model      ProcessModel
	generation uint64
	proc       Process
	lis        Listener
	conn       net.Conn
	peer       PeerIdentity
	failed     bool
	reaped     bool

	rpcMu sync.Mutex // serializes DialAndConfigure on the shared conn
}

type instanceRec struct {
	id         string
	slotKey    string
	generation uint64
	state      instanceState
	closing    bool
	err        error
	result     ActivateResult
	done       chan struct{} // closed on configured or failed
}

func NewHost(cfg Config) *Host {
	if cfg.Launcher == nil {
		cfg.Launcher = NewPlatformLauncher()
	}
	if cfg.Channel == nil {
		cfg.Channel = NewPlatformChannel()
	}
	return &Host{
		cfg:       cfg,
		slots:     map[string]*generationSlot{},
		instances: map[string]*instanceRec{},
	}
}

func (h *Host) SetLaunchProbe(fn func()) {
	h.mu.Lock()
	h.launchNote = fn
	h.mu.Unlock()
}

// ActivateRequest configures one instance against a verified artifact.
type ActivateRequest struct {
	InstanceID  string
	Artifact    *trust.VerifiedArtifact
	Model       ProcessModel
	Sharing     SharingOptions
	FactoryKind string
	ConfigYAML  []byte
	Secrets     backendplugin.SecretBundle
	Policy      backendplugin.RuntimePolicy
	ExpectedUID *int
	ExpectedSID string
	// DialAndConfigure runs only after peer authentication. Secrets are supplied
	// here by the host only after peer success (never via LaunchSpec).
	DialAndConfigure func(ctx context.Context, conn net.Conn, peer PeerIdentity, generation uint64, secrets backendplugin.SecretBundle, configYAML []byte) error
}

type ActivateResult struct {
	Generation uint64
	PID        int
	Peer       PeerIdentity
	Conn       net.Conn
	Cleanup    func() error
}

func (h *Host) Activate(ctx context.Context, req ActivateRequest) (ActivateResult, error) {
	if req.Artifact == nil {
		return ActivateResult{}, ReasonArtifactRequired
	}
	if req.InstanceID == "" {
		return ActivateResult{}, fmt.Errorf("instance id required")
	}
	if req.Model != ProcessModelPerInstance && req.Model != ProcessModelSharedArtifact {
		return ActivateResult{}, ReasonProcessModelViolation
	}
	if req.Model == ProcessModelSharedArtifact && (!req.Sharing.IsolationDeclared || !req.Sharing.ConcurrencyDeclared) {
		return ActivateResult{}, ReasonProcessModelViolation
	}
	if req.DialAndConfigure == nil {
		return ActivateResult{}, ReasonConfigureBeforePeer
	}
	req.Policy.DisableTransportRetries = true

	digest := req.Artifact.DigestHex
	key := OwnershipKey(req.Model, digest, req.InstanceID)

	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return ActivateResult{}, ReasonShuttingDown
	}
	if inst, ok := h.instances[req.InstanceID]; ok {
		switch inst.state {
		case instanceConfigured:
			res := inst.result
			h.mu.Unlock()
			return res, nil
		case instanceFailed:
			err := inst.err
			h.mu.Unlock()
			if err == nil {
				err = ReasonGenerationInvalidated
			}
			return ActivateResult{}, err
		case instancePending:
			done := inst.done
			h.mu.Unlock()
			select {
			case <-ctx.Done():
				return ActivateResult{}, ctx.Err()
			case <-done:
			}
			h.mu.Lock()
			state, err, res := inst.state, inst.err, inst.result
			h.mu.Unlock()
			if state == instanceConfigured {
				return res, nil
			}
			if err == nil {
				err = ReasonGenerationInvalidated
			}
			return ActivateResult{}, err
		}
	}

	inst := &instanceRec{
		id:      req.InstanceID,
		slotKey: key,
		state:   instancePending,
		done:    make(chan struct{}),
	}
	h.instances[req.InstanceID] = inst
	h.mu.Unlock()

	res, err := h.activatePending(ctx, key, req, inst)
	if err != nil {
		h.failInstance(inst, err)
		return ActivateResult{}, err
	}
	h.completeInstance(inst, res)
	return res, nil
}

func (h *Host) activatePending(ctx context.Context, key string, req ActivateRequest, inst *instanceRec) (ActivateResult, error) {
	v, err, _ := h.launchSF.Do(key, func() (any, error) {
		return h.ensureGeneration(ctx, key, req)
	})
	if err != nil {
		return ActivateResult{}, err
	}
	slot, ok := v.(*generationSlot)
	if !ok {
		return ActivateResult{}, fmt.Errorf("processhost: unexpected generation slot type %T", v)
	}

	h.mu.Lock()
	if h.closing || slot.failed {
		h.mu.Unlock()
		return ActivateResult{}, ReasonGenerationInvalidated
	}
	inst.generation = slot.generation
	inst.slotKey = key
	h.mu.Unlock()

	slot.rpcMu.Lock()
	defer slot.rpcMu.Unlock()

	h.mu.Lock()
	if h.closing || slot.failed || inst.state != instancePending {
		state := inst.state
		res := inst.result
		ferr := inst.err
		h.mu.Unlock()
		if state == instanceConfigured {
			return res, nil
		}
		if ferr != nil {
			return ActivateResult{}, ferr
		}
		return ActivateResult{}, ReasonGenerationInvalidated
	}
	h.mu.Unlock()

	if err := req.DialAndConfigure(ctx, slot.conn, slot.peer, slot.generation, req.Secrets, req.ConfigYAML); err != nil {
		_ = h.InvalidateGeneration(key)
		return ActivateResult{}, fmt.Errorf("%w: %v", ReasonHandshakeFailed, err)
	}

	pid := 0
	if slot.proc != nil {
		pid = slot.proc.PID()
	}
	id := req.InstanceID
	return ActivateResult{
		Generation: slot.generation,
		PID:        pid,
		Peer:       slot.peer,
		Conn:       slot.conn,
		Cleanup: func() error {
			return h.CloseInstance(id)
		},
	}, nil
}

func (h *Host) completeInstance(inst *instanceRec, res ActivateResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if inst.state != instancePending {
		return
	}
	inst.state = instanceConfigured
	inst.result = res
	close(inst.done)
}

func (h *Host) failInstance(inst *instanceRec, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if inst.state != instancePending {
		return
	}
	inst.state = instanceFailed
	inst.err = err
	close(inst.done)
}

func (h *Host) ensureGeneration(ctx context.Context, key string, req ActivateRequest) (*generationSlot, error) {
	h.mu.Lock()
	if slot, ok := h.slots[key]; ok && !slot.failed && slot.proc != nil && slot.conn != nil {
		h.mu.Unlock()
		return slot, nil
	}
	h.nextGen++
	gen := h.nextGen
	h.mu.Unlock()

	if note := h.launchNote; note != nil {
		note()
	}

	lis, inherited, err := h.cfg.Channel.Listen(ctx, gen)
	if err != nil {
		return nil, err
	}
	channelFD := 0
	if len(inherited) > 0 {
		channelFD = 3 // ExtraFiles[0] → FD 3 in child
	}
	var extras []string
	if ep, ok := lis.(LaunchEnvProvider); ok {
		extras = ep.LaunchEnv()
	}
	env, err := buildLaunchEnv(h.cfg.AllowEnv, channelFD, extras)
	if err != nil {
		_ = lis.Close()
		closeFiles(inherited)
		return nil, err
	}
	spec := LaunchSpec{
		Artifact:   req.Artifact,
		WorkDir:    h.cfg.WorkDir,
		Env:        env,
		Generation: gen,
		ExtraFiles: inherited,
	}
	proc, err := h.cfg.Launcher.Launch(ctx, spec)
	closeFiles(inherited)
	if err != nil {
		_ = lis.Close()
		return nil, err
	}
	if ul, ok := lis.(interface{ SetExpectedPID(int) }); ok {
		ul.SetExpectedPID(proc.PID())
	}
	if req.ExpectedUID != nil {
		if ul, ok := lis.(interface{ SetExpectedUID(int) }); ok {
			ul.SetExpectedUID(*req.ExpectedUID)
		}
	}
	if req.ExpectedSID != "" {
		if ul, ok := lis.(interface{ SetExpectedSID(string) }); ok {
			ul.SetExpectedSID(req.ExpectedSID)
		}
	}
	if ul, ok := lis.(interface{ SetJobFromProcess(Process) }); ok {
		ul.SetJobFromProcess(proc)
	}
	acceptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, peer, err := lis.Accept(acceptCtx)
	if err != nil {
		_ = proc.Close()
		_ = lis.Close()
		return nil, err
	}
	if err := h.authorizePeer(proc, gen, peer, req.ExpectedUID, req.ExpectedSID); err != nil {
		_ = conn.Close()
		_ = proc.Close()
		_ = lis.Close()
		return nil, err
	}

	slot := &generationSlot{
		key: key, model: req.Model, generation: gen, proc: proc, lis: lis, conn: conn, peer: peer,
	}
	h.mu.Lock()
	h.slots[key] = slot
	h.mu.Unlock()
	return slot, nil
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

func (h *Host) authorizePeer(proc Process, gen uint64, peer PeerIdentity, expectedUID *int, expectedSID string) error {
	if peer.Generation != gen {
		return ReasonStaleGeneration
	}
	if peer.PID <= 0 {
		return ReasonPeerRejected
	}
	if !proc.ContainsPID(peer.PID) {
		if peer.PID == proc.PID() {
			return ReasonPIDReuse
		}
		return ReasonPeerRejected
	}
	if expectedUID != nil && peer.UID != *expectedUID {
		return ReasonPeerRejected
	}
	if expectedSID != "" && peer.SID != expectedSID {
		return ReasonPeerRejected
	}
	return nil
}

func (h *Host) InvalidateGeneration(key string) error {
	h.mu.Lock()
	slot := h.slots[key]
	if slot == nil {
		h.mu.Unlock()
		return nil
	}
	slot.failed = true
	for id, inst := range h.instances {
		if inst.slotKey != key {
			continue
		}
		if inst.state == instancePending {
			inst.state = instanceFailed
			inst.err = ReasonGenerationInvalidated
			select {
			case <-inst.done:
			default:
				close(inst.done)
			}
		}
		delete(h.instances, id)
	}
	h.mu.Unlock()
	return h.reapSlot(slot)
}

// InvalidateProcessGeneration invalidates by process generation ID.
func (h *Host) InvalidateProcessGeneration(generation uint64) error {
	h.mu.Lock()
	var key string
	for k, s := range h.slots {
		if s.generation == generation {
			key = k
			break
		}
	}
	h.mu.Unlock()
	if key == "" {
		return nil
	}
	return h.InvalidateGeneration(key)
}

func (h *Host) CloseInstance(id string) error {
	h.mu.Lock()
	inst, ok := h.instances[id]
	if !ok {
		h.mu.Unlock()
		return nil
	}
	if inst.closing {
		h.mu.Unlock()
		return nil
	}
	inst.closing = true
	delete(h.instances, id)
	key := inst.slotKey
	remain := 0
	for _, other := range h.instances {
		if other.slotKey == key {
			remain++
		}
	}
	slot := h.slots[key]
	h.mu.Unlock()
	if remain == 0 && slot != nil {
		return h.reapSlot(slot)
	}
	return nil
}

func (h *Host) Close() error {
	h.mu.Lock()
	h.closing = true
	slots := make([]*generationSlot, 0, len(h.slots))
	for _, s := range h.slots {
		slots = append(slots, s)
	}
	for _, inst := range h.instances {
		if inst.state == instancePending {
			inst.state = instanceFailed
			inst.err = ReasonShuttingDown
			select {
			case <-inst.done:
			default:
				close(inst.done)
			}
		}
	}
	h.instances = map[string]*instanceRec{}
	h.mu.Unlock()
	var first error
	for i := len(slots) - 1; i >= 0; i-- {
		if err := h.reapSlot(slots[i]); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (h *Host) reapSlot(slot *generationSlot) error {
	h.mu.Lock()
	if slot.reaped {
		h.mu.Unlock()
		return nil
	}
	slot.reaped = true
	slot.failed = true
	delete(h.slots, slot.key)
	h.mu.Unlock()
	if slot.conn != nil {
		_ = slot.conn.Close()
	}
	if slot.lis != nil {
		_ = slot.lis.Close()
	}
	if slot.proc != nil {
		_ = slot.proc.Close()
	}
	return nil
}
