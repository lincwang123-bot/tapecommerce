// Package tronchain verifies only solidified mainnet TRON USDT event evidence.
package tronchain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const Endpoint = "https://api.trongrid.io"
const USDTContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
const contractHex = "a614f803b6fd780986a42c78ec9c7f77e6ded13c"
const transferTopic = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
const Decimals = 6
const maxResponseBytes = 4 << 20

type Request struct {
	TxID, Recipient, AmountUnits string
	EarliestPaidAt               time.Time
	MinBlockNumber               uint64
}
type Head struct {
	Number    uint64
	Hash      string
	Timestamp time.Time
}
type Result struct {
	TxID                         string
	LogIndex                     uint64
	AmountUnits                  string
	PaidAt                       time.Time
	BlockNumber                  uint64
	BlockHash, Recipient, Sender string
	FinalizedBlockNumber         uint64
}
type Error struct{ Kind, Code string }

func (e *Error) Error() string      { return e.Kind + ":" + e.Code }
func fail(kind, code string) *Error { return &Error{kind, code} }

type Client struct{ http *http.Client }

func NewClient(injected *http.Client) *Client {
	c := http.Client{Transport: &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}}
	if injected != nil {
		c = *injected
	}
	if c.Timeout <= 0 || c.Timeout > 10*time.Second {
		c.Timeout = 10 * time.Second
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{&c}
}
func (c *Client) rpc(ctx context.Context, path string, body any, out any) *Error {
	raw, _ := json.Marshal(body)
	r, _ := http.NewRequestWithContext(ctx, "POST", Endpoint+path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	res, e := c.http.Do(r)
	if e != nil {
		return fail("unavailable", "rpc_unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fail("unavailable", "rpc_unavailable")
	}
	raw, e = io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if e != nil || len(raw) > maxResponseBytes || json.Unmarshal(raw, out) != nil {
		return fail("unavailable", "invalid_rpc_response")
	}
	return nil
}

type chainBlock struct {
	ID     string `json:"blockID"`
	Header struct {
		Raw struct {
			Number    uint64 `json:"number"`
			Timestamp int64  `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
	Transactions []struct {
		ID string `json:"txID"`
	} `json:"transactions"`
}

func parseBlock(b *chainBlock) (*Head, *Error) {
	if b == nil {
		return nil, fail("unavailable", "invalid_block")
	}
	hash, ok := NormalizeTxID(b.ID)
	prefix := new(big.Int)
	if ok {
		prefix.SetString(hash[:16], 16)
	}
	if !ok || b.Header.Raw.Number == 0 || !prefix.IsUint64() || prefix.Uint64() != b.Header.Raw.Number || b.Header.Raw.Timestamp <= 0 || b.Header.Raw.Timestamp > time.Now().Add(time.Minute).UnixMilli() {
		return nil, fail("unavailable", "invalid_block")
	}
	return &Head{b.Header.Raw.Number, hash, time.UnixMilli(b.Header.Raw.Timestamp).UTC()}, nil
}
func (c *Client) ReadHead(ctx context.Context) (*Head, *Error) {
	var b *chainBlock
	if e := c.rpc(ctx, "/wallet/getnowblock", map[string]any{}, &b); e != nil {
		return nil, e
	}
	return parseBlock(b)
}

var unitsRE = regexp.MustCompile(`^[1-9][0-9]{0,77}$`)

func abiAddress(s string) ([]byte, bool) {
	if len(s) != 64 || s[:24] != strings.Repeat("0", 24) {
		return nil, false
	}
	b, e := hex.DecodeString(s[24:])
	return append([]byte{0x41}, b...), e == nil
}
func (c *Client) Verify(ctx context.Context, r Request) (*Result, *Error) {
	tx, ok := NormalizeTxID(r.TxID)
	if !ok {
		return nil, fail("invalid_input", "invalid_txid")
	}
	address, ok := NormalizeAddress(r.Recipient)
	if !ok {
		return nil, fail("invalid_input", "invalid_recipient")
	}
	expected, ok := new(big.Int).SetString(r.AmountUnits, 10)
	if !ok || !unitsRE.MatchString(r.AmountUnits) || expected.BitLen() > 256 || r.MinBlockNumber == 0 || r.EarliestPaidAt.IsZero() || r.EarliestPaidAt.Unix() < 0 {
		return nil, fail("invalid_input", "invalid_expectation")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var raw json.RawMessage
	if e := c.rpc(ctx, "/walletsolidity/gettransactioninfobyid", map[string]any{"value": tx, "visible": false}, &raw); e != nil {
		return nil, e
	}
	if string(bytes.TrimSpace(raw)) == "{}" {
		return nil, fail("pending", "not_found_or_pending")
	}
	var receipt struct {
		ID        string `json:"id"`
		Number    uint64 `json:"blockNumber"`
		Timestamp int64  `json:"blockTimeStamp"`
		Receipt   *struct {
			Result string `json:"result"`
		} `json:"receipt"`
		Result string `json:"result"`
		Logs   []struct {
			Address string   `json:"address"`
			Topics  []string `json:"topics"`
			Data    string   `json:"data"`
		} `json:"log"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Receipt == nil || receipt.Number == 0 || receipt.Timestamp <= 0 {
		return nil, fail("unavailable", "invalid_receipt")
	}
	id, ok := NormalizeTxID(receipt.ID)
	if !ok || id != tx {
		return nil, fail("mismatch", "transaction_hash_mismatch")
	}
	var final *chainBlock
	if e := c.rpc(ctx, "/walletsolidity/getnowblock", map[string]any{}, &final); e != nil {
		return nil, e
	}
	head, e := parseBlock(final)
	if e != nil {
		return nil, e
	}
	if head.Number < receipt.Number {
		return nil, fail("pending", "not_finalized")
	}
	var canonical *chainBlock
	if e := c.rpc(ctx, "/walletsolidity/getblockbynum", map[string]any{"num": receipt.Number}, &canonical); e != nil {
		return nil, e
	}
	block, e := parseBlock(canonical)
	if e != nil {
		return nil, e
	}
	if block.Number != receipt.Number || block.Timestamp.UnixMilli() != receipt.Timestamp || head.Timestamp.Before(block.Timestamp) || (head.Number == block.Number && head.Hash != block.Hash) {
		return nil, fail("mismatch", "canonical_block_mismatch")
	}
	found := 0
	for _, item := range canonical.Transactions {
		if strings.EqualFold(item.ID, tx) {
			found++
		}
	}
	if found != 1 {
		return nil, fail("mismatch", "transaction_not_in_block")
	}
	if receipt.Number <= r.MinBlockNumber || block.Timestamp.Before(r.EarliestPaidAt) {
		return nil, fail("mismatch", "payment_before_invoice")
	}
	if receipt.Receipt.Result != "SUCCESS" || (receipt.Result != "" && receipt.Result != "SUCCESS") {
		return nil, fail("mismatch", "transaction_failed")
	}
	var result *Result
	for i, log := range receipt.Logs {
		if strings.ToLower(log.Address) != contractHex || len(log.Topics) == 0 || strings.ToLower(log.Topics[0]) != transferTopic {
			continue
		}
		if len(log.Topics) != 3 || len(log.Data) != 64 {
			return nil, fail("unavailable", "invalid_transfer_log")
		}
		sender, fromOK := abiAddress(log.Topics[1])
		to, toOK := abiAddress(log.Topics[2])
		value, dataErr := hex.DecodeString(log.Data)
		if !fromOK || !toOK || dataErr != nil {
			return nil, fail("unavailable", "invalid_transfer_log")
		}
		amount := new(big.Int).SetBytes(value)
		if encodeAddress(to) != address || amount.Cmp(expected) != 0 {
			continue
		}
		if result != nil {
			return nil, fail("mismatch", "ambiguous_transfer")
		}
		result = &Result{tx, uint64(i), amount.String(), block.Timestamp, block.Number, block.Hash, address, encodeAddress(sender), head.Number}
	}
	if result == nil {
		return nil, fail("mismatch", "transfer_not_matched")
	}
	return result, nil
}
