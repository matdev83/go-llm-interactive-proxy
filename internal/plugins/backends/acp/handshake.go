package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// HandshakeProfile configures initialize / authenticate / session/new for vendor-specific agents.
type HandshakeProfile struct {
	ProtocolVersion int

	// Initialize body fields (merged with defaults).
	ClientCapabilities json.RawMessage // JSON object; nil means {}
	ClientInfo         json.RawMessage // JSON object; nil means {"name":"go-lip","version":"1"}

	// SkipAuthenticate, when true, omits the authenticate RPC (Gemini-style minimal handshake).
	SkipAuthenticate bool
	// AuthenticateParams is the JSON object for authenticate params. Empty means {}.
	AuthenticateParams json.RawMessage

	// SessionNewCwd defaults to "/" if empty.
	SessionNewCwd string
	// SessionNewMCPServers is JSON array (raw); nil means [].
	SessionNewMCPServers json.RawMessage
}

func defaultHandshakeProfile() HandshakeProfile {
	return HandshakeProfile{
		ProtocolVersion: 1,
	}
}

func mergeHandshakeProfile(cfg Config, call *lipapi.Call) HandshakeProfile {
	p := cfg.Handshake
	if p.ProtocolVersion == 0 {
		p.ProtocolVersion = 1
	}
	ext := map[string]json.RawMessage{}
	if call != nil && call.Extensions != nil {
		ext = call.Extensions
	}
	if raw, ok := ext[extInitClientCapabilitiesJSONKey]; ok && len(raw) > 0 && json.Valid(raw) {
		p.ClientCapabilities = raw
	}
	if raw, ok := ext[extInitClientInfoJSONKey]; ok && len(raw) > 0 && json.Valid(raw) {
		p.ClientInfo = raw
	}
	if b, ok := ext[extAuthenticateSkipJSONKey]; ok {
		var skip bool
		if json.Unmarshal(b, &skip) == nil && skip {
			p.SkipAuthenticate = true
		}
	}
	if raw, ok := ext[extAuthenticateMethodIDJSONKey]; ok && len(bytes.TrimSpace(raw)) > 0 {
		var mid string
		if json.Unmarshal(raw, &mid) == nil && strings.TrimSpace(mid) != "" {
			ap := map[string]any{"methodId": strings.TrimSpace(mid)}
			p.AuthenticateParams, _ = json.Marshal(ap)
		}
	}
	if raw, ok := ext[extAuthenticateParamsJSONKey]; ok && len(raw) > 0 && json.Valid(raw) {
		p.AuthenticateParams = raw
	}
	cwdFromExt := jsonStringExt(ext, extCwdJSONKey)
	if cwdFromExt == "" {
		cwdFromExt = jsonStringExt(ext, extWorkspaceJSONKey)
	}
	if cwdFromExt == "" {
		cwdFromExt = strings.TrimSpace(p.SessionNewCwd)
	}
	cwd := strings.TrimSpace(cwdFromExt)
	if cwd != "" {
		p.SessionNewCwd = cwd
	}
	if raw, ok := ext[extMCPServersJSONKey]; ok && len(bytes.TrimSpace(raw)) > 0 && json.Valid(raw) {
		p.SessionNewMCPServers = raw
	}
	if p.SessionNewCwd == "" {
		p.SessionNewCwd = "/"
	}
	return p
}

func jsonStringExt(ext map[string]json.RawMessage, key string) string {
	raw, ok := ext[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func (c *client) initialize(ctx context.Context, hp HandshakeProfile) error {
	id := c.rpcID()
	capObj := map[string]any{}
	if len(hp.ClientCapabilities) > 0 {
		if err := json.Unmarshal(hp.ClientCapabilities, &capObj); err != nil {
			return fmt.Errorf("acp: initialize clientCapabilities: %w", err)
		}
	}
	infoObj := map[string]any{"name": "go-lip", "version": "1"}
	if len(hp.ClientInfo) > 0 {
		if err := json.Unmarshal(hp.ClientInfo, &infoObj); err != nil {
			return fmt.Errorf("acp: initialize clientInfo: %w", err)
		}
	}
	params := map[string]any{
		"protocolVersion":    hp.ProtocolVersion,
		"clientCapabilities": capObj,
		"clientInfo":         infoObj,
	}
	pb, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("acp: initialize marshal params: %w", err)
	}
	req := rpcRequest{JSONRPC: "2.0", ID: jsonRPCNumericID(id), Method: "initialize", Params: pb}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("acp: initialize marshal request: %w", err)
	}
	raw, err := c.t.CallUnary(ctx, body, http.StatusOK)
	if err != nil {
		return fmt.Errorf("acp: initialize call: %w", err)
	}
	var res rpcResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("acp: initialize decode: %w", err)
	}
	if err := rpcErrFromBody("initialize", res.Error); err != nil {
		return err
	}
	return nil
}

func (c *client) authenticate(ctx context.Context, hp HandshakeProfile) error {
	if hp.SkipAuthenticate {
		return nil
	}
	id := c.rpcID()
	params := json.RawMessage("{}")
	if len(hp.AuthenticateParams) > 0 {
		params = hp.AuthenticateParams
	}
	req := rpcRequest{JSONRPC: "2.0", ID: jsonRPCNumericID(id), Method: "authenticate", Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("acp: authenticate marshal request: %w", err)
	}
	raw, err := c.t.CallUnary(ctx, body, http.StatusOK)
	if err != nil {
		return fmt.Errorf("acp: authenticate call: %w", err)
	}
	var res rpcResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("acp: authenticate decode: %w", err)
	}
	if err := rpcErrFromBody("authenticate", res.Error); err != nil {
		return err
	}
	return nil
}

func (c *client) sessionNew(ctx context.Context, hp HandshakeProfile) (string, error) {
	id := c.rpcID()
	mcp := json.RawMessage("[]")
	if len(hp.SessionNewMCPServers) > 0 {
		mcp = hp.SessionNewMCPServers
	}
	var mcpAny any
	if err := json.Unmarshal(mcp, &mcpAny); err != nil {
		return "", fmt.Errorf("acp: mcpServers: %w", err)
	}
	params := map[string]any{
		"cwd":        hp.SessionNewCwd,
		"mcpServers": mcpAny,
	}
	pb, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("acp: session/new marshal params: %w", err)
	}
	req := rpcRequest{JSONRPC: "2.0", ID: jsonRPCNumericID(id), Method: "session/new", Params: pb}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("acp: session/new marshal request: %w", err)
	}
	raw, err := c.t.CallUnary(ctx, body, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("acp: session/new call: %w", err)
	}
	var res rpcResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("acp: session/new decode: %w", err)
	}
	if err := rpcErrFromBody("session/new", res.Error); err != nil {
		return "", err
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res.Result, &out); err != nil {
		return "", fmt.Errorf("acp: session/new result: %w", err)
	}
	if strings.TrimSpace(out.SessionID) == "" {
		return "", fmt.Errorf("acp: session/new: empty sessionId")
	}
	return out.SessionID, nil
}

func runHandshake(ctx context.Context, c *client, hp HandshakeProfile) error {
	if err := c.initialize(ctx, hp); err != nil {
		return err
	}
	if err := c.authenticate(ctx, hp); err != nil {
		return err
	}
	return nil
}
