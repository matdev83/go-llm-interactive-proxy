package host_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

var (
	_ backendplugin.TokenCounter     = (*host.Session)(nil)
	_ backendplugin.BillingFinalizer = (*host.Session)(nil)
)

func TestSession_PublicHostPathCoversLifecycle(t *testing.T) {
	plugin := &publicFake{}
	conn, cleanup := startPublicFake(t, plugin)
	defer cleanup()

	sess, profile, err := host.DialConfiguredSession(context.Background(), conn, "public", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	if profile.EvidenceSource != "public-fake" || !sess.Negotiation().Compatible {
		t.Fatalf("profile=%+v negotiation=%+v", profile, sess.Negotiation())
	}
	if _, err := sess.Resolve(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	stream := &publicStream{ctx: context.Background(), frames: []backendplugin.ClientFrame{
		{Kind: backendplugin.ClientFrameStart, InstanceID: "public", Invocation: validInvocation()},
		{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "public"},
	}}
	if err := sess.Execute(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.out) == 0 {
		t.Fatal("host did not forward execute frames")
	}
	if err := sess.Cancel(context.Background(), *validInvocation()); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := plugin.closeCount.Load(); got != 1 {
		t.Fatalf("close count=%d, want one idempotent close", got)
	}
	if got := plugin.cancelCount.Load(); got != 1 {
		t.Fatalf("cancel count=%d, want one cancel", got)
	}
}

func validInvocation() *backendplugin.Invocation {
	return &backendplugin.Invocation{
		RequestID: "request", AttemptID: "attempt", ALegID: "a-leg", BLegID: "b-leg", CanonicalModelID: "model",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: stringPtr("hello")}}}},
	}
}

func TestSession_PublicHostPathCleansUpWhenInitialResolveFails(t *testing.T) {
	plugin := &publicFake{resolveErr: errors.New("resolve failed")}
	conn, cleanup := startPublicFake(t, plugin)
	defer cleanup()

	_, _, err := host.DialConfiguredSession(context.Background(), conn, "public-error", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err == nil || !stringsContains(err.Error(), "resolve") {
		t.Fatalf("DialConfiguredSession error=%v", err)
	}
	if got := plugin.closeCount.Load(); got != 1 {
		t.Fatalf("failed resolve close count=%d, want one cleanup close", got)
	}
}

func TestSession_PublicHostPathRejectsNilConnection(t *testing.T) {
	if _, _, err := host.DialConfiguredSession(context.Background(), nil, "public", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{}); err == nil {
		t.Fatal("expected nil connection error")
	}
}

func TestSession_ForwardsOptionalAccountingOperations(t *testing.T) {
	plugin := &publicFake{}
	conn, cleanup := startPublicFake(t, plugin)
	defer cleanup()

	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "accounting", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	count, err := sess.CountTokens(context.Background(), backendplugin.CountTokensRequest{
		InstanceID: "accounting", ModelID: "model", Invocation: *validInvocation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if count.InputTokens == nil || *count.InputTokens != 7 || !count.Presence.InputTokens || count.EvidenceQuality != "public-fake" {
		t.Fatalf("count=%+v", count)
	}
	if plugin.lastCount.InstanceID != "accounting" || plugin.lastCount.ModelID != "model" {
		t.Fatalf("count request was not forwarded: %+v", plugin.lastCount)
	}

	final, err := sess.FinalizeBilling(context.Background(), backendplugin.FinalizeBillingRequest{
		InstanceID: "accounting", ALegID: "a-leg", BLegID: "b-leg", ModelID: "model", IdempotencyKey: "idem",
	})
	if err != nil {
		t.Fatal(err)
	}
	if final.Usage.TotalTokens == nil || *final.Usage.TotalTokens != 11 || !final.Usage.Presence.TotalTokens || final.EvidenceQuality != "public-fake" {
		t.Fatalf("final=%+v", final)
	}
	if plugin.lastFinalize.InstanceID != "accounting" || plugin.lastFinalize.IdempotencyKey != "idem" {
		t.Fatalf("finalize request was not forwarded: %+v", plugin.lastFinalize)
	}

	if _, err := sess.CountTokens(context.Background(), backendplugin.CountTokensRequest{}); !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("invalid count error=%v, want %v", err, backendplugin.ErrInvalidInvocation)
	}
	if _, err := sess.FinalizeBilling(context.Background(), backendplugin.FinalizeBillingRequest{}); err == nil {
		t.Fatal("expected invalid finalize request error")
	}
}

func TestSession_ForwardsOptionalAccountingErrors(t *testing.T) {
	countErr := errors.New("count failed")
	finalErr := errors.New("finalize failed")
	plugin := &publicFake{countErr: countErr, finalizeErr: finalErr}
	conn, cleanup := startPublicFake(t, plugin)
	defer cleanup()
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "accounting-errors", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CountTokens(context.Background(), backendplugin.CountTokensRequest{InstanceID: "accounting-errors", Invocation: *validInvocation()}); status.Code(err) != codes.Unknown || !stringsContains(err.Error(), countErr.Error()) {
		t.Fatalf("count err=%v, want unknown count error", err)
	}
	if _, err := sess.FinalizeBilling(context.Background(), backendplugin.FinalizeBillingRequest{InstanceID: "accounting-errors", ALegID: "a", BLegID: "b", ModelID: "m", IdempotencyKey: "i"}); status.Code(err) != codes.Unknown || !stringsContains(err.Error(), finalErr.Error()) {
		t.Fatalf("finalize err=%v, want unknown finalize error", err)
	}
}

func TestSession_DialCleanupClosesTransportOnConfigureAndCloseErrors(t *testing.T) {
	tests := []struct {
		name      string
		configure error
		resolve   error
		close     error
	}{
		{name: "configure", configure: errors.New("configure failed")},
		{name: "resolve", resolve: errors.New("resolve failed")},
		{name: "close instance", resolve: errors.New("resolve failed"), close: errors.New("close failed")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plugin := &publicFake{configureErr: tc.configure, resolveErr: tc.resolve, closeErr: tc.close}
			conn, cleanup := startPublicFake(t, plugin)
			tracked := &trackingConn{Conn: conn}
			defer cleanup()

			_, _, err := host.DialConfiguredSession(context.Background(), tracked, "cleanup", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
			if err == nil {
				t.Fatal("expected dial failure")
			}
			if !tracked.closed.Load() {
				t.Fatal("dial failure leaked the transport connection")
			}
		})
	}
}

func TestSession_CloseErrorRemainsRetryable(t *testing.T) {
	plugin := &publicFake{closeErr: errors.New("close failed")}
	conn, cleanup := startPublicFake(t, plugin)
	defer cleanup()
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "retry-close", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(context.Background()); err == nil {
		t.Fatal("expected Close error")
	}
	if err := sess.Close(context.Background()); err == nil {
		t.Fatal("expected retryable Close error")
	}
	if got := plugin.closeCount.Load(); got != 2 {
		t.Fatalf("close count=%d, want retry after failed close", got)
	}
}

type publicFake struct {
	configureErr error
	resolveErr   error
	closeErr     error
	countErr     error
	finalizeErr  error
	lastCount    backendplugin.CountTokensRequest
	lastFinalize backendplugin.FinalizeBillingRequest
	closeCount   atomic.Int64
	cancelCount  atomic.Int64
}

func (f *publicFake) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorProxyOwnedSessionID,
		PluginID: "public-fake", Version: "v1",
		Features:  []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake", StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true}}},
	}, nil
}

func (f *publicFake) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if f.configureErr != nil {
		return nil, f.configureErr
	}
	return (*publicInstance)(f), nil
}

type publicInstance publicFake

func (f *publicInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	if f.resolveErr != nil {
		return backendplugin.ResolvedProfile{}, f.resolveErr
	}
	return backendplugin.ResolvedProfile{EvidenceSource: "public-fake", Capabilities: backendplugin.CapabilitySummary{Streaming: true}}, nil
}
func (*publicInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}
func (f *publicInstance) Execute(stream backendplugin.ExecuteStream) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if frame.Kind == backendplugin.ClientFrameCancel {
			f.cancelCount.Add(1)
			if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameCancelOutcome, CancelOutcome: &backendplugin.CancelOutcome{Acknowledged: true, Reason: backendplugin.CancelReasonClient}}); err != nil {
				return err
			}
			return stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameTerminal, Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled}})
		}
		if frame.Kind == backendplugin.ClientFrameCloseInput {
			if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameEvent, Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: stringPtr("ok")}}); err != nil {
				return err
			}
			return stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameTerminal, Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess}})
		}
	}
}
func (f *publicInstance) Close(context.Context) error {
	f.closeCount.Add(1)
	return f.closeErr
}

func (f *publicInstance) CountTokens(_ context.Context, req backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	f.lastCount = req
	if f.countErr != nil {
		return backendplugin.CountTokensResponse{}, f.countErr
	}
	value := int64(7)
	return backendplugin.CountTokensResponse{InputTokens: &value, Presence: backendplugin.UsagePresence{InputTokens: true}, EvidenceQuality: "public-fake"}, nil
}

func (f *publicInstance) FinalizeBilling(_ context.Context, req backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	f.lastFinalize = req
	if f.finalizeErr != nil {
		return backendplugin.FinalizeBillingResponse{}, f.finalizeErr
	}
	value := int64(11)
	return backendplugin.FinalizeBillingResponse{Usage: backendplugin.UsageEvidence{TotalTokens: &value, Presence: backendplugin.UsagePresence{TotalTokens: true}}, EvidenceQuality: "public-fake"}, nil
}

type publicStream struct {
	ctx    context.Context
	frames []backendplugin.ClientFrame
	pos    int
	out    []backendplugin.ServerFrame
}

func (s *publicStream) Context() context.Context { return s.ctx }
func (s *publicStream) Recv() (backendplugin.ClientFrame, error) {
	if s.pos == len(s.frames) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	frame := s.frames[s.pos]
	s.pos++
	return frame, nil
}
func (s *publicStream) Send(frame backendplugin.ServerFrame) error {
	s.out = append(s.out, frame)
	return nil
}

type trackingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *trackingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func startPublicFake(t *testing.T, service backendplugin.Service) (net.Conn, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(grpcServer, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}, {Name: backendplugin.FeatureAccountingEvidence, Required: true}},
	}, service))
	go func() { _ = grpcServer.Serve(lis) }()
	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	return conn, func() { _ = conn.Close(); grpcServer.Stop(); _ = lis.Close() }
}

func stringPtr(value string) *string { return &value }
func stringsContains(value, want string) bool {
	return len(value) >= len(want) && contains(value, want)
}
func contains(value, want string) bool {
	for i := 0; i+len(want) <= len(value); i++ {
		if value[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
