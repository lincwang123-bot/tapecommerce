package evmchain

import (
	"context"
	"time"
)

// Retry a whole operation once, never individual evidence requests. Each
// attempt validates chain ID, finality and all transfer expectations afresh.
// Only availability failures qualify; pending/mismatch/invalid input do not.
// The fixed registry is opt-in and cannot accept caller-controlled URLs.
func withFailover[T any](ctx context.Context, c *Client, budget time.Duration, run func(context.Context, *Client) (*T, *Error)) (*T, *Error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if c.backupEndpoint == "" {
		return run(ctx, c)
	}
	first, stop := context.WithTimeout(ctx, budget/2)
	result, err := run(first, c)
	stop()
	if err == nil || ctx.Err() != nil || err.Kind != "unavailable" || (err.Code != "rpc_unavailable" && err.Code != "invalid_rpc_response" && err.Code != "finality_unavailable") {
		return result, err
	}
	// Do not copy Client's atomic sequence or mutate its endpoint: clients can be
	// shared by callers concurrently, and one proof must use exactly one provider.
	backup := &Client{spec: c.spec, configured: c.configured, http: c.http}
	backup.spec.Endpoint = c.backupEndpoint
	return run(ctx, backup)
}

func (c *Client) Verify(ctx context.Context, r Request) (*Result, *Error) {
	return withFailover(ctx, c, 25*time.Second, func(ctx context.Context, attempt *Client) (*Result, *Error) { return attempt.verify(ctx, r) })
}
func (c *Client) ReadHead(ctx context.Context) (*Head, *Error) {
	return withFailover(ctx, c, 15*time.Second, func(ctx context.Context, attempt *Client) (*Head, *Error) { return attempt.readHead(ctx) })
}
func (c *Client) Discover(ctx context.Context, r DiscoverRequest) (*DiscoverPage, *Error) {
	return withFailover(ctx, c, 25*time.Second, func(ctx context.Context, attempt *Client) (*DiscoverPage, *Error) { return attempt.discover(ctx, r) })
}
