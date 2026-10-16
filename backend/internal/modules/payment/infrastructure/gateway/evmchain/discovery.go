package evmchain

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

type DiscoverRequest struct {
	Recipient, AmountUnits, Cursor string
	EarliestPaidAt                 time.Time
	MinBlockNumber                 uint64
}
type DiscoverPage struct {
	TxHashes   []string
	NextCursor string
	Done       bool
}

// NewDiscoveryClient uses a separate closed endpoint registry: BNB's default
// dataseed endpoint disables eth_getLogs. No order/admin supplied URL is accepted.
func NewDiscoveryClient(network Network) *Client {
	c := NewClient(network, nil)
	if network == BSC {
		c.spec.Endpoint = "https://bsc-rpc.publicnode.com"
		if c.backupEndpoint != "" {
			c.backupEndpoint = "https://bsc.drpc.org"
		}
	}
	if network == Ethereum {
		c.spec.Endpoint = "https://eth.drpc.org"
	}
	return c
}

// Discover returns candidates, never payment evidence. A page is an entire
// finalized interval; any invalid/oversized response returns no progress.
func (c *Client) discover(ctx context.Context, r DiscoverRequest) (*DiscoverPage, *Error) {
	recipient, ok := NormalizeAddress(r.Recipient)
	amount, aok := new(big.Int).SetString(r.AmountUnits, 10)
	if !ok || !aok || !decimalUnits.MatchString(r.AmountUnits) || amount.BitLen() > 256 || r.MinBlockNumber == 0 || r.MinBlockNumber == ^uint64(0) || r.EarliestPaidAt.IsZero() {
		return nil, failure("invalid_input", "invalid_discovery_request")
	}
	start := r.MinBlockNumber + 1
	if r.Cursor != "" {
		n, err := strconv.ParseUint(r.Cursor, 10, 64)
		if err != nil || n < start || strconv.FormatUint(n, 10) != r.Cursor {
			return nil, failure("invalid_input", "invalid_discovery_cursor")
		}
		start = n
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var chain string
	if e := c.rpc(ctx, "eth_chainId", []any{}, &chain); e != nil {
		return nil, e
	}
	if chain != c.spec.chainHex {
		return nil, failure("unavailable", "wrong_chain")
	}
	var head *block
	if e := c.rpc(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &head); e != nil {
		return nil, e
	}
	if head == nil {
		return nil, failure("unavailable", "finality_unavailable")
	}
	end, nok := quantity(head.Number)
	_, hok := fixedHex(head.Hash, 32)
	timestamp, tok := quantity(head.Timestamp)
	if !nok || !hok || !tok || timestamp == 0 || timestamp > uint64(time.Now().Add(time.Minute).Unix()) || end == ^uint64(0) {
		return nil, failure("unavailable", "invalid_finalized_block")
	}
	page := &DiscoverPage{TxHashes: []string{}, NextCursor: strconv.FormatUint(start, 10), Done: start > end}
	if start > end {
		return page, nil
	}
	finalized := end
	// Small fixed chunks work within public RPC range limits.
	if end-start >= 100 {
		end = start + 99
	}
	var logs []event
	filter := map[string]any{"fromBlock": fmt.Sprintf("0x%x", start), "toBlock": fmt.Sprintf("0x%x", end), "address": c.spec.TokenContract, "topics": []any{transferTopic, nil, "0x" + strings.Repeat("0", 24) + recipient[2:]}}
	if e := c.rpc(ctx, "eth_getLogs", []any{filter}, &logs); e != nil {
		return nil, e
	}
	if logs == nil || len(logs) >= 1000 {
		return nil, failure("unavailable", "incomplete_discovery_logs")
	}
	seen := map[string]bool{}
	txseen := map[string]bool{}
	blockHashes := map[uint64]string{}
	txBlocks := map[string]uint64{}
	for _, log := range logs {
		addr, adok := fixedHex(log.Address, 20)
		txid, txok := NormalizeTxID(log.TransactionHash)
		blockHash, bhok := fixedHex(log.BlockHash, 32)
		n, bnok := quantity(log.BlockNumber)
		idx, iok := quantity(log.LogIndex)
		if !adok || addr != c.spec.TokenContract || !txok || !bhok || !bnok || n < start || n > end || !iok || log.Removed == nil || *log.Removed || len(log.Topics) != 3 || strings.ToLower(log.Topics[0]) != transferTopic {
			return nil, failure("unavailable", "invalid_discovery_log")
		}
		_, fromok := topicAddress(log.Topics[1])
		to, took := topicAddress(log.Topics[2])
		data, dok := fixedHex(log.Data, 32)
		key := fmt.Sprintf("%d:%d", n, idx)
		if !fromok || !took || to != recipient || !dok || seen[key] {
			return nil, failure("unavailable", "invalid_discovery_log")
		}
		if previous, exists := blockHashes[n]; exists && previous != blockHash {
			return nil, failure("unavailable", "inconsistent_discovery_log")
		}
		if previous, exists := txBlocks[txid]; exists && previous != n {
			return nil, failure("unavailable", "inconsistent_discovery_log")
		}
		blockHashes[n] = blockHash
		txBlocks[txid] = n
		seen[key] = true
		units, _ := new(big.Int).SetString(data[2:], 16)
		if units.Cmp(amount) == 0 && !txseen[txid] {
			page.TxHashes = append(page.TxHashes, txid)
			txseen[txid] = true
		}
	}
	page.NextCursor = strconv.FormatUint(end+1, 10)
	page.Done = end == finalized
	return page, nil
}
