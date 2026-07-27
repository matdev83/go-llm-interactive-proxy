package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/geminicliacp/internal/product"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type Service struct {
	Starter acp.ProcessStarter
}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: 0,
		PluginID: "io.golip.backend.geminicliacp", Version: "0.1.0", BuildID: "localdev",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKind, DisplayName: "Gemini CLI ACP",
			Description:              "External Gemini CLI ACP connector",
			CredentialMode:           backendplugin.CredentialModeNone,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			RoutePrefixes:            []string{FactoryKind},
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true, Vision: true, Reasoning: true},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("geminicliacp: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	pc := cfg.toProduct()
	var eng *product.Engine
	if s != nil && s.Starter != nil {
		eng = product.NewWithStarter(pc, s.Starter)
	} else {
		eng, err = product.New(pc)
		if err != nil {
			return nil, err
		}
	}
	return &instance{eng: eng, id: req.InstanceID}, nil
}

type instance struct {
	eng *product.Engine
	id  string
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true, Vision: true, Reasoning: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind},
		EvidenceSource:           "geminicliacp",
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, max uint32) (backendplugin.ListModelsResponse, error) {
	if i.eng == nil || i.eng.Inventory == nil {
		return backendplugin.ListModelsResponse{}, nil
	}
	snap, err := i.eng.Inventory.LoadModels(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(snap.Models))
	for _, m := range snap.Models {
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: m.CanonicalID, NativeModelID: m.NativeID, FactoryKind: FactoryKind,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		})
		if max > 0 && uint32(len(out)) >= max {
			break
		}
	}
	return backendplugin.ListModelsResponse{Models: out, InventorySource: "geminicliacp", FetchedUnixMS: 1}, nil
}

func (i *instance) Close(context.Context) error {
	if i.eng == nil {
		return nil
	}
	return i.eng.Close()
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	if start.Kind != backendplugin.ClientFrameStart || start.Invocation == nil {
		return fmt.Errorf("%w: expected start", backendplugin.ErrInvalidFrame)
	}
	if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		return err
	}
	call, err := backendplugin.CallFromInvocation(*start.Invocation)
	if err != nil {
		return err
	}
	ctx := context.Background()
	ms, err := i.eng.Open(ctx, &call)
	if err != nil {
		return err
	}
	defer func() { _ = ms.Close() }()
	seq := uint64(1)
	for {
		ev, err := ms.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return stream.Send(backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
				Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
			})
		}
		if err != nil {
			return err
		}
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: backendplugin.CanonicalEventFromLipapi(ev),
		}); err != nil {
			return err
		}
		seq++
	}
}

var (
	_ backendplugin.Service            = (*Service)(nil)
	_ backendplugin.ConfiguredInstance = (*instance)(nil)
)
