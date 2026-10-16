package evmrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
)

type RPC interface {
	Call(context.Context, string, []any, any) error
}

type Client struct {
	endpoint string
	http     *http.Client
	next     atomic.Uint64
}

// NewClient does not make any network request. Configuration must be provided
// by an operator, never by a checkout HTTP request.
func NewClient(endpoint string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return nil, domain.ErrInvalid
	}
	return &Client{endpoint: endpoint, http: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Call(ctx context.Context, method string, params []any, target any) error {
	id := c.next.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return domain.ErrRPC
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return domain.ErrRPC
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return domain.ErrRPC
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return domain.ErrRPC
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if err != nil || len(data) > 2*1024*1024 {
		return domain.ErrRPC
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      uint64          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.ID != id || envelope.Error != nil || len(envelope.Result) == 0 {
		return domain.ErrRPC
	}
	if err := json.Unmarshal(envelope.Result, target); err != nil {
		return fmt.Errorf("%w: malformed %s result", domain.ErrRPC, method)
	}
	return nil
}
