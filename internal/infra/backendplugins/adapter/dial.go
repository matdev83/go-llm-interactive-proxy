package adapter

import (
	"context"
	"fmt"
	"net"
	"slices"
	"sync"

	backendpluginv1 "github.com/matdev83/go-llm-interactive-proxy/api/backendplugin/v1"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DialConfiguredSession negotiates and configures a GRPCSession over an already
// peer-authenticated connection. It is the default DialAndConfigure path for
// discovered host-backed factories.
func DialConfiguredSession(
	ctx context.Context,
	conn net.Conn,
	instanceID, factoryKind string,
	configYAML []byte,
	secrets backendplugin.SecretBundle,
	policy backendplugin.RuntimePolicy,
) (*GRPCSession, backendplugin.ResolvedProfile, error) {
	if conn == nil {
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("adapter: nil conn")
	}
	policy.DisableTransportRetries = true
	var once sync.Once
	dialer := func(context.Context, string) (net.Conn, error) {
		var out net.Conn
		err := net.ErrClosed
		once.Do(func() { out = conn; err = nil })
		if out == nil {
			return nil, err
		}
		return out, nil
	}
	gc, err := grpc.NewClient(
		"passthrough:///backendplugin",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("adapter: grpc dial: %w", err)
	}
	client := backendpluginv1.NewBackendPluginClient(gc)
	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: backendplugin.ProtocolMinorExactOpenResponsesFields,
		HostFeatures: []*backendpluginv1.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
		},
		DisableTransportRetries: true,
	})
	if err != nil {
		_ = gc.Close()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("adapter: negotiate: %w", err)
	}
	if !neg.GetCompatible() {
		_ = gc.Close()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("adapter: negotiate incompatible: %s", neg.GetRejectReason())
	}
	cfgReq := backendplugin.ConfigureRequestToProto(backendplugin.ConfigureRequest{
		InstanceID:       instanceID,
		FactoryKind:      factoryKind,
		ConfigYAML:       configYAML,
		Secrets:          secrets,
		RuntimePolicy:    policy,
		NegotiationToken: neg.GetNegotiationToken(),
	})
	_, err = client.Configure(ctx, cfgReq)
	if err != nil {
		_ = gc.Close()
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("adapter: configure: %w", err)
	}
	enabled := append([]string(nil), neg.GetEnabledFeatures()...)
	slices.Sort(enabled)
	sess := &GRPCSession{
		Client:          client,
		Conn:            gc,
		InstanceID:      instanceID,
		NegotiatedMinor: neg.GetNegotiatedMinor(),
		negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: neg.GetNegotiatedMinor(),
			EnabledFeatures: enabled,
		},
	}
	profile, err := sess.Resolve(ctx, nil)
	if err != nil {
		_ = sess.Close(ctx)
		return nil, backendplugin.ResolvedProfile{}, fmt.Errorf("adapter: resolve: %w", err)
	}
	return sess, profile, nil
}
