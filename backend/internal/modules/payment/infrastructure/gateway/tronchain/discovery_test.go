package tronchain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func historyItem() map[string]any {
	return map[string]any{"transaction_id": testTx, "block_timestamp": int64(1700000020000), "from": USDTContract, "to": USDTContract, "type": "Transfer", "value": "1000000", "token_info": map[string]any{"address": USDTContract, "decimals": 6}}
}
func historyInput() DiscoverRequest {
	r := requestFixture()
	return DiscoverRequest{Recipient: r.Recipient, AmountUnits: r.AmountUnits, EarliestPaidAt: r.EarliestPaidAt, MinBlockNumber: r.MinBlockNumber}
}
func historyFixture(t *testing.T, handler func(url.Values) map[string]any) *Client {
	return NewClient(&http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.trongrid.io" {
			t.Fatal("followed unsafe URL", r.URL)
		}
		var result any
		if r.Method == "POST" && r.URL.Path == "/walletsolidity/getnowblock" {
			result = map[string]any{"blockID": testHash, "block_header": map[string]any{"raw_data": map[string]any{"number": 100, "timestamp": 1700000100000}}}
		} else {
			if r.Method != "GET" || r.URL.Path != "/v1/accounts/"+USDTContract+"/transactions/trc20" {
				t.Fatal("wrong target", r.URL)
			}
			q := r.URL.Query()
			if q.Get("only_confirmed") != "true" || q.Get("only_to") != "true" || q.Get("contract_address") != USDTContract || q.Get("limit") != "200" || q.Get("order_by") != "block_timestamp,asc" || q.Get("min_timestamp") != "1700000000000" || q.Get("max_timestamp") != "1700000040000" {
				t.Fatal("bad/floating query", q)
			}
			result = handler(q)
		}
		b, _ := json.Marshal(result)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})})
}
func historyPage(item map[string]any) map[string]any {
	return map[string]any{"success": true, "data": []any{item}, "meta": map[string]any{"page_size": 1}}
}
func TestDiscoveryHistoryFrozenPaginationAndIgnoresNextURL(t *testing.T) {
	calls := 0
	c := historyFixture(t, func(q url.Values) map[string]any {
		calls++
		p := historyPage(historyItem())
		if calls == 1 {
			p["meta"] = map[string]any{"page_size": 1, "fingerprint": "page two +/", "links": map[string]any{"next": "http://127.0.0.1/private"}}
		} else if q.Get("fingerprint") != "page two +/" {
			t.Fatal(q)
		}
		return p
	})
	r := historyInput()
	p, e := c.Discover(context.Background(), r)
	if e != nil || p.Done || len(p.TxHashes) != 1 || p.NextCursor == "" {
		t.Fatal(p, e)
	}
	r.Cursor = p.NextCursor
	p, e = c.Discover(context.Background(), r)
	if e != nil || !p.Done || len(p.TxHashes) != 1 || p.NextCursor != "" || calls != 2 {
		t.Fatal(p, e, calls)
	}
}
func TestDiscoveryHistoryRejectsBadPageWithoutCursor(t *testing.T) {
	cases := map[string]func(map[string]any){
		"old":             func(p map[string]any) { p["data"].([]any)[0].(map[string]any)["block_timestamp"] = 1699999999999 },
		"future":          func(p map[string]any) { p["data"].([]any)[0].(map[string]any)["block_timestamp"] = 1700000040001 },
		"txid":            func(p map[string]any) { p["data"].([]any)[0].(map[string]any)["transaction_id"] = "bad" },
		"wrong_recipient": func(p map[string]any) { p["data"].([]any)[0].(map[string]any)["to"] = "bad" },
		"wrong_token": func(p map[string]any) {
			p["data"].([]any)[0].(map[string]any)["token_info"] = map[string]any{"address": "wrong", "decimals": 6}
		},
		"missing_fingerprint": func(p map[string]any) {
			p["meta"] = map[string]any{"page_size": 1, "links": map[string]any{"next": "https://evil.test"}}
		},
		"failed":        func(p map[string]any) { p["success"] = false },
		"size_mismatch": func(p map[string]any) { p["meta"] = map[string]any{"page_size": 2} },
		"truncated_full_page": func(p map[string]any) {
			items := make([]any, 200)
			for i := range items {
				items[i] = historyItem()
			}
			p["data"] = items
			p["meta"] = map[string]any{"page_size": 200}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := historyFixture(t, func(url.Values) map[string]any { p := historyPage(historyItem()); mutate(p); return p })
			p, e := c.Discover(context.Background(), historyInput())
			if e == nil || p != nil {
				t.Fatal(p, e)
			}
		})
	}
}
func TestDiscoveryHistoryInclusiveTimeBoundaries(t *testing.T) {
	for _, timestamp := range []int64{1700000000000, 1700000040000} {
		c := historyFixture(t, func(url.Values) map[string]any {
			item := historyItem()
			item["block_timestamp"] = timestamp
			return historyPage(item)
		})
		p, e := c.Discover(context.Background(), historyInput())
		if e != nil || len(p.TxHashes) != 1 {
			t.Fatal(p, e)
		}
	}
}

func TestDiscoveryRoundFinishesDespiteAdvancingSolidHead(t *testing.T) {
	calls := 0
	c := NewClient(&http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		var result any
		if r.Method == "POST" {
			calls++
			result = map[string]any{"blockID": testHash, "block_header": map[string]any{"raw_data": map[string]any{"number": 100, "timestamp": int64(1700001000000) + int64(calls-1)*10000}}}
		} else {
			q := r.URL.Query()
			if calls == 1 && (q.Get("min_timestamp") != "1700000000000" || q.Get("max_timestamp") != "1700000899999") {
				t.Fatal(q)
			}
			if calls == 2 && (q.Get("min_timestamp") != "1700000900000" || q.Get("max_timestamp") != "1700000940000") {
				t.Fatal("round ceiling moved", q)
			}
			result = map[string]any{"success": true, "data": []any{}, "meta": map[string]any{"page_size": 0}}
		}
		b, _ := json.Marshal(result)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})})
	r := historyInput()
	p, e := c.Discover(context.Background(), r)
	if e != nil || p.Done || p.NextCursor == "" {
		t.Fatal(p, e)
	}
	r.Cursor = p.NextCursor
	p, e = c.Discover(context.Background(), r)
	if e != nil || !p.Done || p.NextCursor != "" {
		t.Fatal(p, e)
	}
}
