package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

type client struct {
	t      Transport
	nextID int64
}

func newClient(baseURL string, hc *http.Client) (*client, error) {
	t, err := newHTTPTransport(baseURL, hc)
	if err != nil {
		return nil, err
	}
	return &client{t: t}, nil
}

func (c *client) rpcID() int64 {
	return atomic.AddInt64(&c.nextID, 1)
}

func (c *client) sessionPrompt(ctx context.Context, params map[string]any, rpcID int64) (io.ReadCloser, error) {
	pb, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	reqBody := rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf("%d", rpcID)),
		Method:  "session/prompt",
		Params:  pb,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	return c.t.CallPromptStream(ctx, body)
}
