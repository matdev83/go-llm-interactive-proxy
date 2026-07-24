package stdhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

type genOwnedCloser struct{ closes atomic.Int32 }

func (c *genOwnedCloser) Close() error {
	c.closes.Add(1)
	return nil
}

//nolint:paralleltest // mutates package-level closeProcessServices
func TestShutdownGenerationHost_SkipsProcessCloseWhilePinned(t *testing.T) {
	m := runtimehost.NewManager(4, nil)
	closer := &genOwnedCloser{}
	g := m.PrepareOwned("pinned", closer)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("pin")
	}

	var processCloses atomic.Int32
	orig := closeProcessServices
	closeProcessServices = func(*runtimebundle.ProcessServices) error {
		processCloses.Add(1)
		return nil
	}
	t.Cleanup(func() { closeProcessServices = orig })

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	in := GenerationHostInput{
		Manager: m,
		Process: &runtimebundle.ProcessServices{},
	}
	err := shutdownGenerationHost(ctx, in, 40*time.Millisecond)
	if err == nil {
		t.Fatal("expected pin timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if processCloses.Load() != 0 {
		t.Fatalf("process closes during pin=%d want 0", processCloses.Load())
	}
	if !m.HasOpenGenerations() {
		t.Fatal("expected open generation while pinned")
	}

	pin.Release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := shutdownGenerationHost(ctx2, in, time.Second); err != nil {
		t.Fatalf("retry shutdown: %v", err)
	}
	if processCloses.Load() != 1 {
		t.Fatalf("process closes after release=%d want 1", processCloses.Load())
	}
	if closer.closes.Load() != 1 {
		t.Fatalf("generation closes=%d want 1", closer.closes.Load())
	}
}

//nolint:paralleltest // mutates package-level httpServerShutdown / listenAndServe / closeProcessServices
func TestRunWithGenerationHost_HTTPShutdownFailureDoesNotRetire(t *testing.T) {
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	origClose := closeProcessServices
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
		closeProcessServices = origClose
	})

	started := make(chan struct{})
	stopListen := make(chan struct{})
	var stopListenOnce sync.Once
	stopListenFn := func() { stopListenOnce.Do(func() { close(stopListen) }) }
	listenAndServe = func(*http.Server) error {
		close(started)
		<-stopListen
		return http.ErrServerClosed
	}
	httpServerShutdown = func(context.Context, *http.Server) error {
		return context.DeadlineExceeded
	}
	var processCloses atomic.Int32
	closeProcessServices = func(*runtimebundle.ProcessServices) error {
		processCloses.Add(1)
		return nil
	}

	m := runtimehost.NewManager(2, nil)
	closer := &genOwnedCloser{}
	g := m.PrepareOwned("live", closer)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() {
		stopListenFn()
		closeProcessServices = origClose
		if host.Process != nil && !host.Process.Closed() {
			_ = host.Process.Close()
		}
		if host.Manager != nil {
			_ = host.Manager.ShutdownDetached(context.Background())
		}
		if host.ShutdownTracing != nil {
			_ = host.ShutdownTracing(context.Background())
		}
	})
	host.Config.Server.Address = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          host.Config,
			Log:             host.Logger,
			Manager:         m,
			Process:         host.Process,
			ShutdownTimeout: time.Second,
		})
	}()
	<-started
	cancel()
	got := <-errCh
	stopListenFn()
	if got == nil {
		t.Fatal("expected http shutdown failure")
	}
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("err=%v", got)
	}
	if closer.closes.Load() != 0 {
		t.Fatalf("generation must not close under failed HTTP drain: %d", closer.closes.Load())
	}
	if processCloses.Load() != 0 {
		t.Fatalf("process must not close under failed HTTP drain: %d", processCloses.Load())
	}
	if m.Active() == nil {
		t.Fatal("active generation must remain after failed HTTP drain")
	}
	if host.Process.Closed() {
		t.Fatal("BuildHost process services must remain open")
	}
}

//nolint:paralleltest // mutates package-level httpServerShutdown / listenAndServe
func TestRunWithGenerationHost_CancellationPreservesConcurrentListenerFailure(t *testing.T) {
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	listenerFailure := errors.New("listener failed during cancellation")
	listenStarted := make(chan struct{})
	releaseListener := make(chan struct{})
	listenerReturned := make(chan struct{})
	listenAndServe = func(*http.Server) error {
		close(listenStarted)
		<-releaseListener
		close(listenerReturned)
		return listenerFailure
	}
	shutdownEntered := make(chan struct{})
	releaseShutdown := make(chan struct{})
	httpServerShutdown = func(context.Context, *http.Server) error {
		close(shutdownEntered)
		<-releaseShutdown
		return nil
	}

	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithGenerationHost(ctx, GenerationHostInput{
			Config:          host.Config,
			Log:             host.Logger,
			Manager:         host.Manager,
			Process:         host.Process,
			ShutdownTimeout: 2 * time.Second,
		})
	}()
	<-listenStarted
	cancel()
	<-shutdownEntered
	close(releaseListener)
	<-listenerReturned
	close(releaseShutdown)

	got := <-errCh
	if !errors.Is(got, listenerFailure) {
		t.Fatalf("got %v, want listener failure", got)
	}
	if !host.Process.Closed() {
		t.Fatal("process services must close after the listener exits and HTTP drain succeeds")
	}
}

//nolint:paralleltest // mutates package-level httpServerShutdown / listenAndServe
func TestRunWithGenerationHost_ShutdownListenerErrorStillDrainsHTTP(t *testing.T) {
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	listenAndServe = func(*http.Server) error {
		return errors.New("listener boom")
	}
	var shutdownCalls atomic.Int32
	httpServerShutdown = func(context.Context, *http.Server) error {
		shutdownCalls.Add(1)
		return nil
	}

	cfgPath := filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml")
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	host.Config.Server.Address = "127.0.0.1:0"

	err = RunWithGenerationHost(context.Background(), GenerationHostInput{
		Config:          host.Config,
		Log:             host.Logger,
		Manager:         host.Manager,
		Process:         host.Process,
		ShutdownTimeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("expected serve error")
	}
	if shutdownCalls.Load() != 1 {
		t.Fatalf("http shutdown calls=%d want 1", shutdownCalls.Load())
	}
	if !host.Process.Closed() {
		t.Fatal("process must close after successful HTTP drain on listener error")
	}
	if _, ok := host.Manager.Acquire(); ok {
		t.Fatal("manager must reject acquire after shutdown")
	}
}
