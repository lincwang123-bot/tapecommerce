package selfhost

import (
	"context"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/evmchain"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/tronchain"
)

type discoverer struct {
	evm  map[string]*evmchain.Client
	tron *tronchain.Client
}

func NewDiscoverer() paymentcontract.ChainDiscoverer {
	return &discoverer{evm: map[string]*evmchain.Client{"bsc": evmchain.NewDiscoveryClient(evmchain.BSC), "ethereum": evmchain.NewDiscoveryClient(evmchain.Ethereum), "polygon": evmchain.NewDiscoveryClient(evmchain.Polygon)}, tron: tronchain.NewClient(nil)}
}
func (d *discoverer) Discover(ctx context.Context, network string, r paymentcontract.ChainDiscoverRequest) (*paymentcontract.ChainDiscoverPage, *paymentcontract.ChainError) {
	if network == "tron" {
		p, e := d.tron.Discover(ctx, tronchain.DiscoverRequest{Recipient: r.Recipient, AmountUnits: r.AmountUnits, EarliestPaidAt: r.EarliestPaidAt, MinBlockNumber: r.MinBlockNumber, Cursor: r.Cursor})
		if e != nil {
			return nil, &paymentcontract.ChainError{Kind: e.Kind, Code: e.Code}
		}
		return &paymentcontract.ChainDiscoverPage{TxHashes: p.TxHashes, NextCursor: p.NextCursor, Done: p.Done}, nil
	}
	c, ok := d.evm[network]
	if !ok {
		return nil, &paymentcontract.ChainError{Kind: "invalid_input", Code: "unsupported_network"}
	}
	p, e := c.Discover(ctx, evmchain.DiscoverRequest{Recipient: r.Recipient, AmountUnits: r.AmountUnits, EarliestPaidAt: r.EarliestPaidAt, MinBlockNumber: r.MinBlockNumber, Cursor: r.Cursor})
	if e != nil {
		return nil, &paymentcontract.ChainError{Kind: e.Kind, Code: e.Code}
	}
	return &paymentcontract.ChainDiscoverPage{TxHashes: p.TxHashes, NextCursor: p.NextCursor, Done: p.Done}, nil
}

// Discover is an optional capability of the verifier's concrete implementation.
// Independent clients keep discovery-specific RPC choices out of verification.
func (v *verifier) Discover(ctx context.Context, network string, r paymentcontract.ChainDiscoverRequest) (*paymentcontract.ChainDiscoverPage, *paymentcontract.ChainError) {
	return NewDiscoverer().Discover(ctx, network, r)
}

var _ paymentcontract.ChainDiscoverer = (*verifier)(nil)
