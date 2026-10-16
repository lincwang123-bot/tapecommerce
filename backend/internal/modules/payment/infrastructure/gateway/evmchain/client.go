// Package evmchain verifies finalized transfer evidence on three fixed EVM networks.
// It never binds an order, credits a ledger, signs, or broadcasts transactions.
package evmchain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const Endpoint = "https://bsc-dataseed.bnbchain.org"
const USDTContract = "0x55d398326f99059ff775485246999027b3197955"
const Decimals = 18
const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
const maxResponseBytes = 2 << 20

type Request struct {
	// Mandatory invoice-creation barrier: only later blocks may settle it.
	MinBlockNumber               uint64
	TxID, Recipient, AmountUnits string
	EarliestPaidAt               time.Time
}
type Result struct {
	TxID                 string    `json:"txid"`
	LogIndex             uint64    `json:"log_index"`
	AmountUnits          string    `json:"amount_units"`
	PaidAt               time.Time `json:"paid_at"`
	BlockNumber          uint64    `json:"block_number"`
	BlockHash            string    `json:"block_hash"`
	Recipient            string    `json:"recipient"`
	Sender               string    `json:"sender"`
	FinalizedBlockNumber uint64    `json:"finalized_block_number"`
}
type Error struct{ Kind, Code string }

func (e *Error) Error() string         { return e.Kind + ":" + e.Code }
func failure(kind, code string) *Error { return &Error{Kind: kind, Code: code} }

type Client struct {
	backupEndpoint string
	spec           NetworkSpec
	configured     bool
	http           *http.Client
	sequence       atomic.Uint64
}

// NewClient copies an optional test client, always enforcing a bounded timeout
// and rejecting redirects. Production uses a dedicated, proxy-free transport
// without connection reuse, so signed/read requests are never transport-retried.
func NewClient(network Network, injected *http.Client) *Client {
	spec, configured := Specification(network)
	c := http.Client{Transport: &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}}
	if injected != nil {
		c = *injected
	}
	if c.Timeout <= 0 || c.Timeout > 10*time.Second {
		c.Timeout = 10 * time.Second
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := &Client{http: &c, spec: spec, configured: configured}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SELFHOST_RPC_FAILOVER_ENABLED")), "true") {
		switch network {
		case BSC:
			client.backupEndpoint = "https://bsc-rpc.publicnode.com"
		case Polygon:
			client.backupEndpoint = "https://polygon-bor-rpc.publicnode.com"
		}
	}
	return client
}

var hexQuantity = regexp.MustCompile(`^0x(0|[1-9a-fA-F][0-9a-fA-F]*)$`)
var decimalUnits = regexp.MustCompile(`^[1-9][0-9]{0,77}$`)

func fixedHex(s string, n int) (string, bool) {
	if len(s) != 2+n*2 || !strings.HasPrefix(s, "0x") {
		return "", false
	}
	_, err := hex.DecodeString(s[2:])
	return strings.ToLower(s), err == nil
}
func NormalizeTxID(s string) (string, bool) { return fixedHex(s, 32) }
func NormalizeAddress(s string) (string, bool) {
	normalized, ok := fixedHex(s, 20)
	return normalized, ok && normalized != "0x"+strings.Repeat("0", 40)
}
func quantity(s string) (uint64, bool) {
	if len(s) > 18 || !hexQuantity.MatchString(s) {
		return 0, false
	}
	n, ok := new(big.Int).SetString(s[2:], 16)
	if !ok || n.Sign() < 0 || !n.IsUint64() {
		return 0, false
	}
	return n.Uint64(), true
}

func (c *Client) rpc(ctx context.Context, method string, params []any, dest any) *Error {
	if !c.configured {
		return failure("invalid_input", "unsupported_network")
	}
	id := c.sequence.Add(1)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, "POST", c.spec.Endpoint, bytes.NewReader(body))
	if err != nil {
		return failure("unavailable", "rpc_unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return failure("unavailable", "rpc_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return failure("unavailable", "rpc_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return failure("unavailable", "invalid_rpc_response")
	}
	var envelope struct {
		Version string          `json:"jsonrpc"`
		ID      uint64          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Version != "2.0" || envelope.ID != id || len(envelope.Result) == 0 || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		return failure("unavailable", "invalid_rpc_response")
	}
	if json.Unmarshal(envelope.Result, dest) != nil {
		return failure("unavailable", "invalid_rpc_response")
	}
	return nil
}

type block struct{ Number, Hash, Timestamp string }
type event struct {
	Address                                                 string
	Topics                                                  []string
	Data, LogIndex, TransactionHash, BlockHash, BlockNumber string
	Removed                                                 *bool
}
type receipt struct {
	TransactionHash, BlockHash, BlockNumber, Status string
	Logs                                            []event
}

func topicAddress(topic string) (string, bool) {
	normalized, ok := fixedHex(topic, 32)
	if !ok || normalized[2:26] != strings.Repeat("0", 24) {
		return "", false
	}
	return "0x" + normalized[26:], true
}

// Verify accepts exactly one matching Transfer log from a successful receipt
// whose canonical block is at or below this RPC's finalized head. An RPC that
// lacks finalized support fails closed; latest/confirmation-count fallback is
// deliberately absent. RPC honesty remains an explicit external trust boundary.
func (c *Client) verify(ctx context.Context, input Request) (*Result, *Error) {
	txid, ok := NormalizeTxID(input.TxID)
	if !ok {
		return nil, failure("invalid_input", "invalid_txid")
	}
	recipient, ok := NormalizeAddress(input.Recipient)
	if !ok {
		return nil, failure("invalid_input", "invalid_recipient")
	}
	amount, valid := new(big.Int).SetString(input.AmountUnits, 10)
	if !valid || !decimalUnits.MatchString(input.AmountUnits) || amount.BitLen() > 256 || input.EarliestPaidAt.IsZero() || input.EarliestPaidAt.Unix() < 0 || input.MinBlockNumber == 0 {
		return nil, failure("invalid_input", "invalid_expectation")
	}
	// Bound the complete multi-RPC operation as well as each individual call.
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var chain string
	if err := c.rpc(ctx, "eth_chainId", []any{}, &chain); err != nil {
		return nil, err
	}
	if chain != c.spec.chainHex {
		return nil, failure("unavailable", "wrong_chain")
	}
	var rec *receipt
	if err := c.rpc(ctx, "eth_getTransactionReceipt", []any{txid}, &rec); err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, failure("pending", "not_found_or_pending")
	}
	hash, hashOK := fixedHex(rec.BlockHash, 32)
	number, numberOK := quantity(rec.BlockNumber)
	returnedTx, txOK := NormalizeTxID(rec.TransactionHash)
	if !hashOK || !numberOK || number == 0 || !txOK {
		return nil, failure("unavailable", "invalid_receipt")
	}
	if returnedTx != txid {
		return nil, failure("mismatch", "transaction_hash_mismatch")
	}
	var finalized *block
	if err := c.rpc(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &finalized); err != nil {
		return nil, err
	}
	if finalized == nil {
		return nil, failure("unavailable", "finality_unavailable")
	}
	finalNumber, ok := quantity(finalized.Number)
	finalHash, fhOK := fixedHex(finalized.Hash, 32)
	finalTimestamp, ftOK := quantity(finalized.Timestamp)
	if !ok || !fhOK || !ftOK || finalTimestamp == 0 {
		return nil, failure("unavailable", "invalid_finalized_block")
	}
	if finalNumber < number {
		return nil, failure("pending", "not_finalized")
	}
	var canonical *block
	if err := c.rpc(ctx, "eth_getBlockByNumber", []any{rec.BlockNumber, false}, &canonical); err != nil {
		return nil, err
	}
	if canonical == nil {
		return nil, failure("unavailable", "canonical_block_unavailable")
	}
	canonicalNumber, nOK := quantity(canonical.Number)
	canonicalHash, hOK := fixedHex(canonical.Hash, 32)
	timestamp, tOK := quantity(canonical.Timestamp)
	if !nOK || !hOK || !tOK || timestamp == 0 || timestamp > uint64(time.Now().Add(time.Minute).Unix()) {
		return nil, failure("unavailable", "invalid_canonical_block")
	}
	if finalTimestamp < timestamp {
		return nil, failure("unavailable", "inconsistent_finality_time")
	}
	if canonicalNumber != number || canonicalHash != hash || (finalNumber == number && finalHash != hash) {
		return nil, failure("mismatch", "canonical_block_mismatch")
	}
	paidAt := time.Unix(int64(timestamp), 0).UTC()
	if paidAt.Before(input.EarliestPaidAt) {
		return nil, failure("mismatch", "payment_too_old")
	}
	if number <= input.MinBlockNumber {
		return nil, failure("mismatch", "payment_before_invoice_block")
	}
	if rec.Status != "0x1" {
		return nil, failure("mismatch", "transaction_failed")
	}
	var matched *Result
	seen := map[uint64]bool{}
	for _, log := range rec.Logs {
		contract, ok := fixedHex(log.Address, 20)
		if !ok || contract != c.spec.TokenContract || len(log.Topics) == 0 || strings.ToLower(log.Topics[0]) != transferTopic {
			continue
		}
		if len(log.Topics) != 3 {
			return nil, failure("unavailable", "invalid_transfer_log")
		}
		from, fromOK := topicAddress(log.Topics[1])
		to, toOK := topicAddress(log.Topics[2])
		encoded, dataOK := fixedHex(log.Data, 32)
		index, indexOK := quantity(log.LogIndex)
		logTx, ltOK := fixedHex(log.TransactionHash, 32)
		logHash, lhOK := fixedHex(log.BlockHash, 32)
		logNumber, lnOK := quantity(log.BlockNumber)
		if !fromOK || !toOK || !dataOK || !indexOK || !ltOK || !lhOK || !lnOK || log.Removed == nil {
			return nil, failure("unavailable", "invalid_transfer_log")
		}
		if *log.Removed || logTx != txid || logHash != hash || logNumber != number || seen[index] {
			return nil, failure("mismatch", "inconsistent_transfer_log")
		}
		seen[index] = true
		units, _ := new(big.Int).SetString(encoded[2:], 16)
		if to != recipient || units.Cmp(amount) != 0 {
			continue
		}
		if matched != nil {
			return nil, failure("mismatch", "ambiguous_transfer")
		}
		matched = &Result{TxID: txid, LogIndex: index, AmountUnits: units.String(), PaidAt: paidAt, BlockNumber: number, BlockHash: hash, Recipient: recipient, Sender: from, FinalizedBlockNumber: finalNumber}
	}
	if matched == nil {
		return nil, failure("mismatch", "transfer_not_matched")
	}
	return matched, nil
}

// Head records a canonical latest block observed before freezing an invoice.
// This is a lower-bound creation barrier, never evidence of finalized payment.
type Head struct {
	Number    uint64
	Hash      string
	Timestamp time.Time
}

func (c *Client) readHead(ctx context.Context) (*Head, *Error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var chain string
	if err := c.rpc(ctx, "eth_chainId", []any{}, &chain); err != nil {
		return nil, err
	}
	if chain != c.spec.chainHex {
		return nil, failure("unavailable", "wrong_chain")
	}
	var head *block
	if err := c.rpc(ctx, "eth_getBlockByNumber", []any{"latest", false}, &head); err != nil {
		return nil, err
	}
	if head == nil {
		return nil, failure("unavailable", "head_unavailable")
	}
	number, nOK := quantity(head.Number)
	hash, hOK := fixedHex(head.Hash, 32)
	timestamp, tOK := quantity(head.Timestamp)
	if !nOK || number == 0 || !hOK || !tOK || timestamp == 0 || timestamp > uint64(time.Now().Add(time.Minute).Unix()) {
		return nil, failure("unavailable", "invalid_head")
	}
	return &Head{Number: number, Hash: hash, Timestamp: time.Unix(int64(timestamp), 0).UTC()}, nil
}
