package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type Service struct {
	// Starter injects a deterministic ProcessStarter (tests).
	Starter product.ProcessStarter
	// ModelListSource overrides bridge model discovery (tests).
	ModelListSource product.ModelListSource
}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: 0,
		PluginID: PluginID, Version: "0.1.0", BuildID: "localdev",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     FactoryKind,
			DisplayName:              "Cursor SDK",
			Description:              "External experimental Cursor SDK local-agent connector",
			CredentialMode:           backendplugin.CredentialModeStatic,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			RoutePrefixes:            []string{FactoryKind},
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true, Reasoning: true},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("cursorsdk: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	secret := ""
	if req.Secrets.Values != nil {
		secret = string(req.Secrets.Values["api_key"])
	}
	in, err := cfg.toProductInput(secret)
	if err != nil {
		return nil, err
	}
	normalized, err := product.Normalize(in, "")
	if err != nil {
		return nil, err
	}
	sc := product.NewScaffold(normalized).WithInstanceID(req.InstanceID)
	if s != nil && s.Starter != nil {
		sc = sc.WithProcessStarter(s.Starter)
	}
	if s != nil && s.ModelListSource != nil {
		sc = sc.WithModelListSource(s.ModelListSource)
	}
	be := sc.Backend()
	if tracking, ok := be.ModelInventory.(interface {
		AcceptInventory([]modelinventory.Model)
		LoadModels(context.Context) (modelinventory.Snapshot, error)
	}); ok {
		snap, loadErr := tracking.LoadModels(context.Background())
		if loadErr == nil && len(snap.Models) > 0 {
			tracking.AcceptInventory(snap.Models)
		}
	}
	return &instance{be: be}, nil
}

type instance struct {
	be execbackend.Backend
}

func (i *instance) Resolve(_ context.Context, model *string) (backendplugin.ResolvedProfile, error) {
	caps := backendplugin.CapabilitySummary{Streaming: true, Reasoning: true}
	if i != nil && i.be.ResolveCaps != nil && model != nil {
		call := lipapi.Call{Route: lipapi.RouteIntent{Selector: strings.TrimSpace(*model)}}
		cand := routing.AttemptCandidate{
			Primary: routing.Primary{Backend: FactoryKind, Model: strings.TrimSpace(*model)},
			Key:     FactoryKind + ":" + strings.TrimSpace(*model),
		}
		bc := i.be.ResolveCaps(context.Background(), call, cand)
		_, streaming := bc[lipapi.CapabilityStreaming]
		_, reasoning := bc[lipapi.CapabilityReasoning]
		_, vision := bc[lipapi.CapabilityVision]
		_, tools := bc[lipapi.CapabilityTools]
		caps = backendplugin.CapabilitySummary{
			Streaming: streaming,
			Reasoning: reasoning,
			Vision:    vision,
			Tools:     tools,
		}
	}
	return backendplugin.ResolvedProfile{
		Capabilities:             caps,
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind},
		EvidenceSource:           FactoryKind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, max uint32) (backendplugin.ListModelsResponse, error) {
	if i == nil || i.be.ModelInventory == nil {
		return backendplugin.ListModelsResponse{}, nil
	}
	snap, err := i.be.ModelInventory.LoadModels(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(snap.Models))
	for _, m := range snap.Models {
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: m.CanonicalID,
			NativeModelID:    m.NativeID,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true, Reasoning: true},
		})
		if max > 0 && uint32(len(out)) >= max {
			break
		}
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: FactoryKind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func (i *instance) Close(context.Context) error {
	if i == nil || i.be.Close == nil {
		return nil
	}
	return i.be.Close()
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	if i == nil || i.be.Open == nil {
		return fmt.Errorf("cursorsdk: backend not configured")
	}
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
	model := strings.TrimSpace(start.Invocation.CanonicalModelID)
	if model == "" {
		model = strings.TrimSpace(start.Invocation.NativeModelID)
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: FactoryKind, Model: model},
		Key:     FactoryKind + ":" + model,
	}
	ctx := context.Background()
	ms, err := i.be.Open(ctx, call, cand)
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
