package backendplugin

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	MetaOperation          = "lip.operation"
	MetaDeliveryMode       = "lip.delivery_mode"
	MetaTransportMode      = "lip.transport_mode"
	MetaRouteParamPrefix   = "route.param."
	MetaExtensionPrefix    = "ext."
	maxMetaExtensionBytes  = 64 << 10
	maxMetaExtensionFields = 64
)

// ApplyCallWireMetadata projects operation, delivery, transport, route params,
// ordered item wire fields, and Call.Extensions into an invocation for cross-process execute.
func ApplyCallWireMetadata(inv *Invocation, call lipapi.Call, routeParams map[string]string) {
	applyLegacyCallWireMetadata(inv, call, routeParams)
	ApplyOrderedItemWire(inv, call)
}

// ApplyCallWireMetadataWithNegotiation projects wire metadata and enforces ordered-item and
// exact OpenResponses ABI gates before execution.
func ApplyCallWireMetadataWithNegotiation(inv *Invocation, call lipapi.Call, routeParams map[string]string, neg Negotiation) error {
	if err := RequireOrderedItemABISupport(neg, call); err != nil {
		return err
	}
	if err := RequireExactOpenResponsesABISupport(neg, call); err != nil {
		return err
	}
	if call.HasItemAuthority() {
		if err := checkOrderedItemContentABIRepresentable(call.Items); err != nil {
			return err
		}
	}
	ApplyCallWireMetadata(inv, call, routeParams)
	return nil
}

func applyLegacyCallWireMetadata(inv *Invocation, call lipapi.Call, routeParams map[string]string) {
	if inv == nil {
		return
	}
	if inv.SafeMetadata == nil {
		inv.SafeMetadata = make(map[string]string)
	}
	if op := string(call.Invocation.Operation); op != "" {
		inv.SafeMetadata[MetaOperation] = op
	}
	if dm := string(call.Invocation.DeliveryMode); dm != "" {
		inv.SafeMetadata[MetaDeliveryMode] = dm
	}
	if tm := string(call.Invocation.TransportMode); tm != "" {
		inv.SafeMetadata[MetaTransportMode] = tm
	}
	for k, v := range routeParams {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		inv.SafeMetadata[MetaRouteParamPrefix+k] = v
	}
	n := 0
	for key, raw := range call.Extensions {
		if n >= maxMetaExtensionFields {
			break
		}
		key = strings.TrimSpace(key)
		if key == "" || len(raw) == 0 || len(raw) > maxMetaExtensionBytes {
			continue
		}
		inv.SafeMetadata[MetaExtensionPrefix+key] = string(raw)
		n++
	}
}

// RestoreCallWireMetadata rebuilds lipapi.Call wire fields from SafeMetadata.
func RestoreCallWireMetadata(call *lipapi.Call, meta map[string]string) {
	if call == nil || len(meta) == 0 {
		return
	}
	if op := strings.TrimSpace(meta[MetaOperation]); op != "" {
		call.Invocation.Operation = lipapi.Operation(op)
	}
	if dm := strings.TrimSpace(meta[MetaDeliveryMode]); dm != "" {
		call.Invocation.DeliveryMode = lipapi.DeliveryMode(dm)
	}
	if tm := strings.TrimSpace(meta[MetaTransportMode]); tm != "" {
		call.Invocation.TransportMode = lipapi.TransportMode(tm)
	}
	ext := call.Extensions
	if ext == nil {
		ext = make(map[string]json.RawMessage)
	}
	for k, v := range meta {
		if !strings.HasPrefix(k, MetaExtensionPrefix) {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, MetaExtensionPrefix))
		if name == "" || v == "" {
			continue
		}
		ext[name] = json.RawMessage(v)
	}
	if len(ext) > 0 {
		call.Extensions = ext
	}
}

// RouteParam returns a route.param.* value from SafeMetadata.
func RouteParam(meta map[string]string, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(meta) == 0 {
		return ""
	}
	return strings.TrimSpace(meta[MetaRouteParamPrefix+name])
}
