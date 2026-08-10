package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCSession is a host-side ExecuteSession over a BackendPlugin gRPC client.
type GRPCSession struct {
	Client          backendpluginv1.BackendPluginClient
	Conn            *grpc.ClientConn
	InstanceID      string
	NegotiatedMinor uint32
	negotiation     backendplugin.Negotiation
	closeMu         sync.Mutex
	closed          bool
}

// Negotiation returns the protocol negotiation outcome bound at dial time.
func (s *GRPCSession) Negotiation() backendplugin.Negotiation {
	return s.negotiation
}

func (s *GRPCSession) Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	req := &backendpluginv1.ResolveProfileRequest{InstanceId: s.InstanceID}
	if modelID != nil {
		req.ModelId = modelID
	}
	resp, err := s.Client.ResolveProfile(ctx, req)
	if err != nil {
		return backendplugin.ResolvedProfile{}, err
	}
	return backendplugin.ResolvedProfileFromProto(resp.GetProfile())
}

func (s *GRPCSession) ListModels(ctx context.Context, maxModels uint32) (backendplugin.ListModelsResponse, error) {
	resp, err := s.Client.ListModels(ctx, &backendpluginv1.ListModelsRequest{
		InstanceId: s.InstanceID,
		MaxModels:  maxModels,
	})
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	return backendplugin.ListModelsResponseFromProto(resp)
}

// Cancel sends a start followed by a cancel frame through the host stream.
func (s *GRPCSession) Cancel(ctx context.Context, inv backendplugin.Invocation) error {
	stream := &cancelStream{ctx: ctx, frames: []backendplugin.ClientFrame{
		{Kind: backendplugin.ClientFrameStart, InstanceID: s.InstanceID, Invocation: &inv},
		{Kind: backendplugin.ClientFrameCancel, InstanceID: s.InstanceID, CancelReason: backendplugin.CancelReasonClient},
	}}
	err := s.Execute(stream)
	if !stream.cancelSent {
		return fmt.Errorf("adapter: cancellation frame was not transmitted")
	}
	return err
}

func (s *GRPCSession) Close(ctx context.Context) error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closeMu.Unlock()
	_, err := s.Client.CloseInstance(ctx, &backendpluginv1.CloseInstanceRequest{InstanceId: s.InstanceID})
	if err != nil {
		return err
	}
	s.closeMu.Lock()
	s.closed = true
	s.closeMu.Unlock()
	if s.Conn != nil {
		_ = s.Conn.Close()
	}
	return nil
}

func (s *GRPCSession) CountTokens(ctx context.Context, req backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	p, err := backendplugin.CountTokensRequestToProto(req)
	if err != nil {
		return backendplugin.CountTokensResponse{}, err
	}
	resp, err := s.Client.CountTokens(ctx, p)
	if err != nil {
		return backendplugin.CountTokensResponse{}, err
	}
	return backendplugin.CountTokensResponseFromProto(resp)
}

func (s *GRPCSession) FinalizeBilling(ctx context.Context, req backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	resp, err := s.Client.FinalizeBilling(ctx, backendplugin.FinalizeBillingRequestToProto(req))
	if err != nil {
		return backendplugin.FinalizeBillingResponse{}, err
	}
	return backendplugin.FinalizeBillingResponseFromProto(resp)
}

func (s *GRPCSession) Execute(stream backendplugin.ExecuteStream) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	gs, err := s.Client.Execute(ctx)
	if err != nil {
		return mapGRPCSessionError(err, false)
	}

	errCh := make(chan error, 1)
	var once sync.Once
	report := func(err error) {
		if err == nil || errors.Is(err, io.EOF) || ctx.Err() != nil {
			return
		}
		once.Do(func() { errCh <- err })
	}

	go func() {
		// Input EOF closes the client half only; canceling here would abort the
		// server response before terminal frames arrive.
		defer func() { _ = gs.CloseSend() }()
		for {
			fr, err := stream.Recv()
			if err != nil {
				report(err)
				return
			}
			if err := backendplugin.ValidateClientFrameBounds(fr); err != nil {
				report(err)
				return
			}
			msg, err := backendplugin.ClientFrameToProto(fr)
			if err != nil {
				report(err)
				return
			}
			if err := gs.Send(msg); err != nil {
				report(err)
				return
			}
		}
	}()

	terminalSeen := false
	for {
		msg, err := gs.Recv()
		if err != nil {
			cancel()
			if peer := firstErr(errCh); peer != nil {
				return mapGRPCSessionError(peer, terminalSeen)
			}
			return mapGRPCSessionError(err, terminalSeen)
		}
		fr, err := backendplugin.ServerFrameFromProto(msg)
		if err != nil {
			cancel()
			return ProtocolViolation(err)
		}
		if fr.Kind == backendplugin.ServerFrameTerminal {
			terminalSeen = true
		}
		if err := stream.Send(fr); err != nil {
			cancel()
			return err
		}
	}
}

func firstErr(ch <-chan error) error {
	select {
	case e := <-ch:
		return e
	default:
		return nil
	}
}

func mapGRPCSessionError(err error, terminalSeen bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Canceled {
		return context.Canceled
	}
	if errors.Is(err, io.EOF) {
		if terminalSeen {
			return nil
		}
		return TransportDeath(io.EOF)
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unknown, codes.Internal, codes.Aborted:
			if terminalSeen {
				return nil
			}
			return TransportDeath(err)
		}
	}
	if !terminalSeen {
		return TransportDeath(err)
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

var (
	_ ExecuteSession = (*GRPCSession)(nil)
	_ interface {
		Cancel(context.Context, backendplugin.Invocation) error
	} = (*GRPCSession)(nil)
	_ OptionalTokenCounter     = (*GRPCSession)(nil)
	_ OptionalBillingFinalizer = (*GRPCSession)(nil)
)
