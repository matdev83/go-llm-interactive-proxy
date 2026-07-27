package processhost

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TestLauncher is an in-process launcher for host unit tests (not production).
type TestLauncher struct {
	Launches      atomic.Int64
	PID           int
	Fail          error
	ContainFail   bool // ContainsPID always false (simulates PID reuse / dead child)
	LastSpec      LaunchSpec
	LastProcess   Process
	OnLaunch      func(LaunchSpec)
	StderrPayload string
}

func (t *TestLauncher) Launch(_ context.Context, spec LaunchSpec) (Process, error) {
	t.Launches.Add(1)
	t.LastSpec = spec
	if t.OnLaunch != nil {
		t.OnLaunch(spec)
	}
	if t.Fail != nil {
		return nil, t.Fail
	}
	pid := t.PID
	if pid == 0 {
		pid = 4242
	}
	proc := &testProc{
		pid: pid, gen: spec.Generation, containFail: t.ContainFail,
		stderr: t.StderrPayload,
	}
	t.LastProcess = proc
	return proc, nil
}

type testProc struct {
	pid         int
	gen         uint64
	containFail bool
	killed      atomic.Bool
	waited      atomic.Bool
	stderr      string
}

func (p *testProc) PID() int           { return p.pid }
func (p *testProc) Generation() uint64 { return p.gen }
func (p *testProc) Stdout() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

func (p *testProc) Stderr() io.ReadCloser {
	return io.NopCloser(strings.NewReader(p.stderr))
}

func (p *testProc) ContainsPID(pid int) bool {
	if p.containFail {
		return false
	}
	return pid == p.pid
}
func (p *testProc) SignalKill() error { p.killed.Store(true); return nil }
func (p *testProc) GracefulStop(timeout time.Duration) error {
	_ = timeout
	return p.SignalKill()
}

func (p *testProc) Wait() error {
	p.waited.CompareAndSwap(false, true)
	return nil
}

func (p *testProc) Close() error {
	_ = p.SignalKill()
	return p.Wait()
}
func (p *testProc) Killed() bool { return p.killed.Load() }
func (p *testProc) Waited() bool { return p.waited.Load() }

// TestChannel is a peer-gated in-memory channel for unit tests.
type TestChannel struct {
	mu         sync.Mutex
	PeerPID    int
	PeerUID    int
	RejectPeer bool
	StaleGen   bool
	Accepts    atomic.Int64
}

func (t *TestChannel) Listen(_ context.Context, generation uint64) (Listener, []*os.File, error) {
	c1, c2 := net.Pipe()
	lis := &testListener{
		ch: t, generation: generation, server: c1, client: c2,
	}
	return lis, nil, nil
}

type testListener struct {
	ch          *TestChannel
	generation  uint64
	server      net.Conn
	client      net.Conn
	expected    int
	expectedUID int
	once        sync.Once
}

func (l *testListener) SetExpectedPID(pid int) { l.expected = pid }
func (l *testListener) SetExpectedUID(uid int) { l.expectedUID = uid }

func (l *testListener) Accept(ctx context.Context) (net.Conn, PeerIdentity, error) {
	select {
	case <-ctx.Done():
		_ = l.Close()
		return nil, PeerIdentity{}, ctx.Err()
	default:
	}
	l.ch.Accepts.Add(1)
	if l.ch.RejectPeer {
		_ = l.Close()
		return nil, PeerIdentity{}, ReasonPeerRejected
	}
	pid := l.ch.PeerPID
	if pid == 0 {
		pid = l.expected
	}
	gen := l.generation
	if l.ch.StaleGen {
		gen++
	}
	uid := l.ch.PeerUID
	if l.expectedUID != 0 && uid == 0 {
		uid = l.expectedUID
	}
	return l.server, PeerIdentity{PID: pid, UID: uid, Generation: gen}, nil
}

func (l *testListener) Close() error {
	var err error
	l.once.Do(func() {
		err1 := l.server.Close()
		err2 := l.client.Close()
		if err1 != nil {
			err = err1
		} else {
			err = err2
		}
	})
	return err
}

// CookiePlaintextChannel refuses cookie/plaintext loopback bootstrap.
type CookiePlaintextChannel struct{}

func (CookiePlaintextChannel) Listen(context.Context, uint64) (Listener, []*os.File, error) {
	return nil, nil, ReasonCookieAuthRejected
}

// UnsupportedChannel refuses with the stable unsupported_channel reason.
type UnsupportedChannel struct{}

func (UnsupportedChannel) Listen(context.Context, uint64) (Listener, []*os.File, error) {
	return nil, nil, ReasonUnsupportedChannel
}
