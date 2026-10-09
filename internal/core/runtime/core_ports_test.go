package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationprojection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
)

type corePortsReader struct{ conversationprojection.Reader }

type corePortsWireDetector struct{ CompactionWireDetector }

func TestCorePorts_OverlayPreservesPresenceAndReaderProvenance(t *testing.T) {
	reader := &corePortsReader{}
	base := CorePorts{ConversationViewReader: reader, ConversationReaderStockOrigin: true}
	var boxedNil *corePortsReader
	merged := base.Overlay(CorePorts{ConversationViewReader: boxedNil})
	if merged.ConversationViewReader != boxedNil || merged.ConversationReaderStockOrigin {
		t.Fatal("boxed nil must replace the reader and clear its stock provenance")
	}
	if empty := base.Overlay(CorePorts{}); empty.ConversationViewReader != reader || !empty.ConversationReaderStockOrigin {
		t.Fatal("absent overlay must retain the reader and provenance")
	}
	if base.ConversationViewReader != reader || !base.ConversationReaderStockOrigin {
		t.Fatal("overlay mutated its input")
	}
	called := false
	bootstrap := ConversationBootstrap(func(context.Context, string, func() (InitialModelIntent, error)) error { called = true; return nil })
	if err := (CorePorts{}).Overlay(CorePorts{ConversationBootstrap: bootstrap}).ConversationBootstrap(t.Context(), "a", nil); err != nil || !called {
		t.Fatal("supplied callback was not retained")
	}
}

func TestCorePorts_CensusPreservesBootstrapAndTypedNilWireDetector(t *testing.T) {
	bootstrap := ConversationBootstrap(func(context.Context, string, func() (InitialModelIntent, error)) error { return nil })
	var detector *corePortsWireDetector
	p := CorePorts{ConversationBootstrap: bootstrap, CompactionDetector: detector}
	census := largebody.NewStandardDependencyCensus("ports")
	p.AddToCensus(&census, false, false, false)
	if !census.Ports.SteeringWriterFactoryOccupied || !census.Ports.CompactionDetectorOccupied || !census.Ports.CompactionDetectorWireSupported {
		t.Fatal("bootstrap blocks before stored steering; boxed detector retains its wire contract")
	}
	if len(census.ExtraPorts) != 0 {
		t.Fatal("existing classified ports changed the dependency inventory")
	}
}
