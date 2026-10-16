package tronchain

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
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
type historyCursor struct {
	Start, End, Last, Until int64
	Fingerprint             string
	Pages                   int
}

const historyLimit = 200
const historyWindowMS = int64(15 * time.Minute / time.Millisecond)

func encodeCursor(c historyCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Discover freezes time bounds throughout fingerprint pagination. links.next is
// only a completeness signal, never a URL to fetch. After catching up, restart
// from the invoice timestamp next round to catch delayed history indexing.
func (c *Client) Discover(ctx context.Context, r DiscoverRequest) (*DiscoverPage, *Error) {
	recipient, ok := NormalizeAddress(r.Recipient)
	amount, aok := new(big.Int).SetString(r.AmountUnits, 10)
	earliest := r.EarliestPaidAt.UnixMilli()
	if !ok || !aok || !unitsRE.MatchString(r.AmountUnits) || amount.BitLen() > 256 || r.EarliestPaidAt.IsZero() || earliest < 0 || r.MinBlockNumber == 0 {
		return nil, fail("invalid_input", "invalid_discovery_request")
	}
	cur := historyCursor{Start: earliest}
	if r.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(r.Cursor)
		if e != nil || len(raw) > 8192 || json.Unmarshal(raw, &cur) != nil || cur.Start < earliest || cur.End < cur.Start || cur.Until < cur.End || cur.End-cur.Start >= historyWindowMS || cur.Last < 0 || (cur.Last != 0 && (cur.Last < cur.Start || cur.Last > cur.End)) || cur.Pages < 0 || cur.Pages > 10000 || len(cur.Fingerprint) > 4096 {
			return nil, fail("invalid_input", "invalid_discovery_cursor")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var rawHead *chainBlock
	if e := c.rpc(ctx, "/walletsolidity/getnowblock", map[string]any{}, &rawHead); e != nil {
		return nil, e
	}
	head, e := parseBlock(rawHead)
	if e != nil {
		return nil, e
	}
	// Give the indexer a bounded grace interval, while still replaying later.
	safeCeiling := head.Timestamp.Add(-time.Minute).UnixMilli()
	if r.Cursor == "" {
		cur.Until = safeCeiling
	}
	if cur.Until > safeCeiling {
		return nil, fail("unavailable", "history_finality_regressed")
	}
	ceiling := cur.Until
	page := &DiscoverPage{TxHashes: []string{}}
	if cur.Start > ceiling {
		page.Done = true
		return page, nil
	}
	if cur.End == 0 {
		cur.End = cur.Start + historyWindowMS - 1
		if cur.End > ceiling {
			cur.End = ceiling
		}
	}
	if cur.End > ceiling {
		return nil, fail("unavailable", "history_finality_regressed")
	}
	q := url.Values{"only_confirmed": {"true"}, "only_to": {"true"}, "limit": {strconv.Itoa(historyLimit)}, "contract_address": {USDTContract}, "order_by": {"block_timestamp,asc"}, "min_timestamp": {strconv.FormatInt(cur.Start, 10)}, "max_timestamp": {strconv.FormatInt(cur.End, 10)}}
	if cur.Fingerprint != "" {
		q.Set("fingerprint", cur.Fingerprint)
	}
	request, err := http.NewRequestWithContext(ctx, "GET", Endpoint+"/v1/accounts/"+recipient+"/transactions/trc20?"+q.Encode(), nil)
	if err != nil {
		return nil, fail("unavailable", "history_unavailable")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fail("unavailable", "history_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fail("unavailable", "history_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, fail("unavailable", "invalid_history_response")
	}
	var result struct {
		Success bool `json:"success"`
		Data    []struct {
			TxID                  string `json:"transaction_id"`
			Timestamp             int64  `json:"block_timestamp"`
			From, To, Type, Value string
			Token                 struct {
				Address  string
				Decimals *int
			} `json:"token_info"`
		} `json:"data"`
		Meta *struct {
			Fingerprint string
			PageSize    *int `json:"page_size"`
			Links       struct{ Next string }
		} `json:"meta"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Success || result.Data == nil || result.Meta == nil || len(result.Data) > historyLimit || (result.Meta.PageSize != nil && *result.Meta.PageSize != len(result.Data)) {
		return nil, fail("unavailable", "invalid_history_response")
	}
	seen := map[string]bool{}
	for _, item := range result.Data {
		txid, txok := NormalizeTxID(item.TxID)
		from, fromok := NormalizeAddress(item.From)
		to, took := NormalizeAddress(item.To)
		units, uok := new(big.Int).SetString(item.Value, 10)
		if !txok || !fromok || from == "" || !took || to != recipient || item.Token.Address != USDTContract || item.Token.Decimals == nil || *item.Token.Decimals != Decimals || item.Type != "Transfer" || !uok || !(item.Value == "0" || unitsRE.MatchString(item.Value)) || units.BitLen() > 256 || item.Timestamp < cur.Start || item.Timestamp > cur.End || item.Timestamp < cur.Last {
			return nil, fail("unavailable", "invalid_history_transfer")
		}
		cur.Last = item.Timestamp
		if units.Cmp(amount) == 0 && !seen[txid] {
			page.TxHashes = append(page.TxHashes, txid)
			seen[txid] = true
		}
	}
	fp := result.Meta.Fingerprint
	hasNext := result.Meta.Links.Next != ""
	if hasNext || fp != "" {
		if fp == "" || fp == cur.Fingerprint || len(fp) > 4096 || len(result.Data) == 0 || cur.Pages >= 10000 {
			return nil, fail("unavailable", "incomplete_history_page")
		}
		cur.Fingerprint = fp
		cur.Pages++
		page.NextCursor = encodeCursor(cur)
		return page, nil
	}
	// A full page without a continuation may have been truncated: never advance.
	if len(result.Data) == historyLimit {
		return nil, fail("unavailable", "incomplete_history_page")
	}
	if cur.End >= ceiling {
		page.Done = true
		return page, nil
	}
	// Freeze the next bounded window now, retaining fixed parameters on its pages.
	next := historyCursor{Start: cur.End + 1, End: cur.End + historyWindowMS, Until: cur.Until}
	if next.End > ceiling {
		next.End = ceiling
	}
	page.NextCursor = encodeCursor(next)
	return page, nil
}
