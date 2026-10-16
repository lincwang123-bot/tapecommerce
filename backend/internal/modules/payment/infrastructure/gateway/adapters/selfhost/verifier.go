// Package selfhost adapts fixed-chain read-only evidence clients to the payment port.
package selfhost

import (
	"context"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/evmchain"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/tronchain"
)

type verifier struct {
	evm  map[string]*evmchain.Client
	tron *tronchain.Client
}

func New() paymentcontract.ChainVerifier {
	return &verifier{map[string]*evmchain.Client{"bsc": evmchain.NewClient(evmchain.BSC, nil), "ethereum": evmchain.NewClient(evmchain.Ethereum, nil), "polygon": evmchain.NewClient(evmchain.Polygon, nil)}, tronchain.NewClient(nil)}
}
func (v *verifier) Network(channel string) (paymentcontract.ChainNetwork, bool) {
	network := map[string]string{"usdtbep20": "bsc", "usdterc20": "ethereum", "usdtpolygon": "polygon", "usdttrc20": "tron"}[channel]
	if network == "tron" {
		return paymentcontract.ChainNetwork{Network: network, Label: "TRON USDT (TRC20)", TokenContract: tronchain.USDTContract, Decimals: 6}, true
	}
	spec, ok := evmchain.Specification(evmchain.Network(network))
	if !ok {
		return paymentcontract.ChainNetwork{}, false
	}
	label := map[string]string{"bsc": "BSC USDT (BEP20)", "ethereum": "Ethereum USDT (ERC20)", "polygon": "Polygon USDT (USDT0)"}[network]
	return paymentcontract.ChainNetwork{Network: network, ChainID: spec.ChainID, Label: label, TokenContract: spec.TokenContract, Decimals: spec.Decimals}, true
}
func (v *verifier) NormalizeAddress(network, address string) (string, bool) {
	if network == "tron" {
		return tronchain.NormalizeAddress(address)
	}
	if _, ok := v.evm[network]; !ok {
		return "", false
	}
	return evmchain.NormalizeAddress(address)
}
func (v *verifier) ReadHead(ctx context.Context, network string) (*paymentcontract.ChainHead, *paymentcontract.ChainError) {
	if network == "tron" {
		r, e := v.tron.ReadHead(ctx)
		if e != nil {
			return nil, &paymentcontract.ChainError{Kind: e.Kind, Code: e.Code}
		}
		return &paymentcontract.ChainHead{Number: r.Number, Hash: r.Hash, Timestamp: r.Timestamp}, nil
	}
	c, ok := v.evm[network]
	if !ok {
		return nil, &paymentcontract.ChainError{Kind: "invalid_input", Code: "unsupported_network"}
	}
	r, e := c.ReadHead(ctx)
	if e != nil {
		return nil, &paymentcontract.ChainError{Kind: e.Kind, Code: e.Code}
	}
	return &paymentcontract.ChainHead{Number: r.Number, Hash: r.Hash, Timestamp: r.Timestamp}, nil
}
func (v *verifier) Verify(ctx context.Context, network string, request paymentcontract.ChainVerifyRequest) (*paymentcontract.ChainEvidence, *paymentcontract.ChainError) {
	if network == "tron" {
		r, e := v.tron.Verify(ctx, tronchain.Request{TxID: request.TxID, Recipient: request.Recipient, AmountUnits: request.AmountUnits, EarliestPaidAt: request.EarliestPaidAt, MinBlockNumber: request.MinBlockNumber})
		if e != nil {
			return nil, &paymentcontract.ChainError{Kind: e.Kind, Code: e.Code}
		}
		return &paymentcontract.ChainEvidence{TxID: r.TxID, LogIndex: r.LogIndex, AmountUnits: r.AmountUnits, PaidAt: r.PaidAt, BlockNumber: r.BlockNumber, BlockHash: r.BlockHash, Recipient: r.Recipient, Sender: r.Sender, FinalizedBlockNumber: r.FinalizedBlockNumber}, nil
	}
	c, ok := v.evm[network]
	if !ok {
		return nil, &paymentcontract.ChainError{Kind: "invalid_input", Code: "unsupported_network"}
	}
	r, e := c.Verify(ctx, evmchain.Request{TxID: request.TxID, Recipient: request.Recipient, AmountUnits: request.AmountUnits, EarliestPaidAt: request.EarliestPaidAt, MinBlockNumber: request.MinBlockNumber})
	if e != nil {
		return nil, &paymentcontract.ChainError{Kind: e.Kind, Code: e.Code}
	}
	return &paymentcontract.ChainEvidence{TxID: r.TxID, LogIndex: r.LogIndex, AmountUnits: r.AmountUnits, PaidAt: r.PaidAt, BlockNumber: r.BlockNumber, BlockHash: r.BlockHash, Recipient: r.Recipient, Sender: r.Sender, FinalizedBlockNumber: r.FinalizedBlockNumber}, nil
}
