// Package host provides the supported public construction path for executable
// backend-plugin contract clients.
package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ProtocolViolationError preserves the distinction between malformed protocol
// frames and transport failures for internal host adapters.
type ProtocolViolationError struct{ Err error }

func (e *ProtocolViolationError) Error() string { return e.Err.Error() }
func (e *ProtocolViolationError) Unwrap() error { return e.Err }

// Session is the public host-facing configured connector boundary.
type Session struct {
	client     backendpluginv1.BackendPluginClient
	conn       *grpc.ClientConn
	instanceID string

	negotiation backendplugin.Negotiation
	lifecycleMu sync.Mutex
	closeMu     sync.Mutex
	closed      bool
}

// NewSession binds an already-created gRPC client to a host session. It is
// intended for standard-distribution composition tests; connector authors
// should normally use DialConfiguredSession.
func NewSessionForTesting(client backendpluginv1.BackendPluginClient, conn *grpc.ClientConn, instanceID string, negotiation backendplugin.Negotiation) *Session {
	return &Session{
		client: client, conn: conn, instanceID: instanceID,
		negotiation: negotiation,
	}
}

// DialConfiguredSession negotiates, configures, and resolves a plugin over an
// already authenticated connection. The caller owns conn until Close.
func DialConfiguredSession(ctx context.Context, conn net.Conn, instanceID, factoryKind string, configYAML []byte, secrets backendplugin.SecretBundle, policy backendplugin.RuntimePolicy) (*Session, backendplugin.ResolvedProfile, error) {
	if conn == nil {
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: nil conn")
	}
	policy.DisableTransportRetries = true
	var once sync.Once
	dialer := func(context.Context, string) (net.Conn, error) {
		var out net.Conn
		err := net.ErrClosed
		once.Do(func() { out, err = conn, nil })
		if out == nil {
			return nil, err
		}
		return out, nil
	}
	gc, err := grpc.NewClient("passthrough:///backendplugin", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: grpc dial: %w", err)
	}
	cleanup := func() { _ = gc.Close() }
	client := backendpluginv1.NewBackendPluginClient(gc)
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorSemanticExtensions,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
		DisableTransportRetries: true,
	}
	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: offer.Minor,
		HostFeatures: []*backendpluginv1.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
		DisableTransportRetries: true,
	})
	if err != nil {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: negotiate: %w", err)
	}
	if !neg.GetCompatible() {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: negotiate incompatible: %s", neg.GetRejectReason())
	}
	negotiated, err := backendplugin.NegotiationFromNegotiateResponse(neg)
	if err != nil {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: negotiate response: %w", err)
	}
	if err := backendplugin.ValidateNegotiationResult(offer, negotiated); err != nil {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: negotiate response validation: %w", err)
	}
	_, err = client.Configure(ctx, backendplugin.ConfigureRequestToProto(backendplugin.ConfigureRequest{
		InstanceID: instanceID, FactoryKind: factoryKind, ConfigYAML: configYAML,
		Secrets: secrets, RuntimePolicy: policy, NegotiationToken: neg.GetNegotiationToken(),
	}))
	if err != nil {
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: configure: %w", err)
	}
	negotiatedEnabled := append([]string(nil), negotiated.EnabledFeatures...)
	slices.Sort(negotiatedEnabled)
	s := &Session{client: client, conn: gc, instanceID: instanceID, negotiation: backendplugin.Negotiation{
		Compatible: negotiated.Compatible, NegotiatedMinor: negotiated.NegotiatedMinor,
		EnabledFeatures: negotiatedEnabled, PluginMajor: negotiated.PluginMajor,
		PluginMinor: negotiated.PluginMinor, PluginFeatures: negotiated.PluginFeatures, TransportPolicy: negotiated.TransportPolicy,
	}}
	profile, err := s.Resolve(ctx, nil)
	if err != nil {
		_ = s.Close(ctx)
		cleanup()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("host: resolve: %w", err)
	}
	return s, profile, nil
}

// Negotiation returns the protocol outcome bound at dial time.
func (s *Session) Negotiation() backendplugin.Negotiation { return s.negotiation }

func (s *Session) Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	req := &backendpluginv1.ResolveProfileRequest{InstanceId: s.instanceID}
	if modelID != nil {
		req.ModelId = modelID
	}
	resp, err := s.client.ResolveProfile(ctx, req)
	if err != nil {
		return backendplugin.ResolvedProfile{}, err
	}
	return backendplugin.ResolvedProfileFromProto(resp.GetProfile())
}

func (s *Session) ListModels(ctx context.Context, maxModels uint32) (backendplugin.ListModelsResponse, error) {
	resp, err := s.client.ListModels(ctx, &backendpluginv1.ListModelsRequest{InstanceId: s.instanceID, MaxModels: maxModels})
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	return backendplugin.ListModelsResponseFromProto(resp)
}

// Execute forwards the public DTO stream through the gRPC host session.
func (s *Session) Execute(stream backendplugin.ExecuteStream) error {
	// Serialize lifecycle operations so Close cannot tear down the transport
	// underneath an active execute RPC. The optional stream closer still handles
	// input-pump cleanup when Execute itself is ending.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.closeMu.Lock()
	closed := s.closed
	s.closeMu.Unlock()
	if closed {
		return fmt.Errorf("host: session is closed")
	}

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	var (
		streamCloseOnce sync.Once
		closer          backendplugin.OptionalExecuteStreamCloser
	)
	if c, ok := stream.(backendplugin.OptionalExecuteStreamCloser); ok {
		closer = c
	}
	closeStream := func() {
		streamCloseOnce.Do(func() {
			if closer != nil {
				_ = closer.Close()
			}
		})
	}
	defer closeStream()

	gs, err := s.client.Execute(ctx)
	if err != nil {
		return mapSessionError(err, false)
	}
	errCh := make(chan error, 1)
	var reportOnce sync.Once
	report := func(e error) {
		if e != nil && !errors.Is(e, io.EOF) && ctx.Err() == nil {
			reportOnce.Do(func() { errCh <- e })
		}
	}
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		defer func() { _ = gs.CloseSend() }()
		for {
			frame, recvErr := stream.Recv()
			if recvErr != nil {
				report(recvErr)
				return
			}
			if recvErr = backendplugin.ValidateClientFrameBounds(frame); recvErr != nil {
				report(recvErr)
				return
			}
			msg, convErr := backendplugin.ClientFrameToProto(frame)
			if convErr != nil {
				report(convErr)
				return
			}
			if sendErr := gs.Send(msg); sendErr != nil {
				report(sendErr)
				return
			}
		}
	}()
	var closeOnce sync.Once
	closePump := func() {
		closeOnce.Do(func() {
			cancel()
			closeStream()
			// Legacy ExecuteStream implementations must observe their own
			// Context, but the host cannot cancel that caller-owned context.
			// Only wait when the optional closer gives us a reliable unblock
			// seam; otherwise return without turning a non-cooperative stream
			// into a host deadlock.
			if closer != nil {
				<-pumpDone
			}
		})
	}
	defer closePump()

	terminal := false
	for {
		msg, recvErr := gs.Recv()
		if recvErr != nil {
			closePump()
			if ctxErr := stream.Context().Err(); ctxErr != nil {
				return ctxErr
			}
			if peer := firstSessionError(errCh); peer != nil {
				return mapSessionError(peer, terminal)
			}
			return mapSessionError(recvErr, terminal)
		}
		frame, convErr := backendplugin.ServerFrameFromProto(msg)
		if convErr != nil {
			closePump()
			return &ProtocolViolationError{Err: convErr}
		}
		if terminal {
			closePump()
			return &ProtocolViolationError{Err: fmt.Errorf("session: unexpected server frame after terminal")}
		}
		if frame.Kind == backendplugin.ServerFrameTerminal {
			terminal = true
		}
		if sendErr := stream.Send(frame); sendErr != nil {
			closePump()
			return sendErr
		}
	}
}

func (s *Session) Cancel(ctx context.Context, invocation backendplugin.Invocation) error {
	stream := &cancelStream{ctx: ctx, frames: []backendplugin.ClientFrame{{Kind: backendplugin.ClientFrameStart, InstanceID: s.instanceID, Invocation: &invocation}, {Kind: backendplugin.ClientFrameCancel, InstanceID: s.instanceID, CancelReason: backendplugin.CancelReasonClient}}}
	err := s.Execute(stream)
	if !stream.cancelSent {
		return fmt.Errorf("adapter: cancellation frame was not transmitted")
	}
	return err
}

func (s *Session) Close(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	if _, err := s.client.CloseInstance(ctx, &backendpluginv1.CloseInstanceRequest{InstanceId: s.instanceID}); err != nil {
		return err
	}
	s.closed = true
	if s.conn != nil {
		_ = s.conn.Close()
	}
	return nil
}

// CountTokens forwards the optional token-counting operation.
func (s *Session) CountTokens(ctx context.Context, req backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	wire, err := backendplugin.CountTokensRequestToProto(req)
	if err != nil {
		return backendplugin.CountTokensResponse{}, err
	}
	resp, err := s.client.CountTokens(ctx, wire)
	if err != nil {
		return backendplugin.CountTokensResponse{}, err
	}
	return backendplugin.CountTokensResponseFromProto(resp)
}

// FinalizeBilling forwards the optional billing-finalization operation.
func (s *Session) FinalizeBilling(ctx context.Context, req backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	resp, err := s.client.FinalizeBilling(ctx, backendplugin.FinalizeBillingRequestToProto(req))
	if err != nil {
		return backendplugin.FinalizeBillingResponse{}, err
	}
	return backendplugin.FinalizeBillingResponseFromProto(resp)
}

func firstSessionError(ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return nil
	}
}

func mapSessionError(err error, terminal bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if st, ok := status.FromError(err); ok {
		if st.Code() == codes.Canceled {
			return context.Canceled
		}
		if st.Code() == codes.DeadlineExceeded && !terminal {
			return context.DeadlineExceeded
		}
		if terminal && (st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded || st.Code() == codes.Unknown || st.Code() == codes.Internal || st.Code() == codes.Aborted) {
			return nil
		}
	}
	if errors.Is(err, io.EOF) {
		if terminal {
			return nil
		}
		return backendplugin.ModeError{Code: "transport_death"}
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unknown, codes.Internal, codes.Aborted:
			if terminal {
				return nil
			}
			return backendplugin.ModeError{Code: "transport_death"}
		}
	}
	if !terminal {
		return backendplugin.ModeError{Code: "transport_death"}
	}
	return err
}

type cancelStream struct {
	ctx        context.Context
	frames     []backendplugin.ClientFrame
	pos        int
	cancelSent bool
}

func (s *cancelStream) Context() context.Context { return s.ctx }
func (s *cancelStream) Recv() (backendplugin.ClientFrame, error) {
	if s.pos >= len(s.frames) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	frame := s.frames[s.pos]
	s.pos++
	if frame.Kind == backendplugin.ClientFrameCancel {
		s.cancelSent = true
	}
	return frame, nil
}
func (s *cancelStream) Send(backendplugin.ServerFrame) error { return nil }

var _ interface {
	Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error)
	ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error)
	Execute(backendplugin.ExecuteStream) error
	Cancel(context.Context, backendplugin.Invocation) error
	Close(context.Context) error
	Negotiation() backendplugin.Negotiation
} = (*Session)(nil)

var (
	_ backendplugin.TokenCounter     = (*Session)(nil)
	_ backendplugin.BillingFinalizer = (*Session)(nil)
)
