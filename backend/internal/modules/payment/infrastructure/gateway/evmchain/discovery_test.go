package evmchain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func discoveryFixture(t *testing.T, mutate func(map[string]any)) *Client {
	return NewClient(BSC, &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		var req struct {
			ID     uint64
			Method string
			Params []json.RawMessage
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Fatal("request")
		}
		var result any
		switch req.Method {
		case "eth_chainId":
			result = "0x38"
		case "eth_getBlockByNumber":
			result = map[string]any{"number": "0x64", "hash": blockHash, "timestamp": "0x6553f164"}
		case "eth_getLogs":
			var filter struct {
				FromBlock, ToBlock, Address string
				Topics                      []any
			}
			json.Unmarshal(req.Params[0], &filter)
			if filter.FromBlock != "0x64" || filter.ToBlock != "0x64" || filter.Address != USDTContract || len(filter.Topics) != 3 || filter.Topics[0] != transferTopic || filter.Topics[1] != nil || filter.Topics[2] != "0x"+strings.Repeat("0", 24)+recipient[2:] {
				t.Fatalf("bad filter: %+v", filter)
			}
			log := receiptFixture()["logs"].([]any)[0].(map[string]any)
			if mutate != nil {
				mutate(log)
			}
			result = []any{log}
		default:
			t.Fatal(req.Method)
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})})
}
func discoverInput() DiscoverRequest {
	r := expectation()
	return DiscoverRequest{Recipient: r.Recipient, AmountUnits: r.AmountUnits, EarliestPaidAt: r.EarliestPaidAt, MinBlockNumber: r.MinBlockNumber}
}
func TestDiscoveryReturnsCandidatesAndSafeCursor(t *testing.T) {
	p, e := discoveryFixture(t, nil).Discover(context.Background(), discoverInput())
	if e != nil || len(p.TxHashes) != 1 || p.TxHashes[0] != tx || p.NextCursor != "101" || !p.Done {
		t.Fatal(p, e)
	}
}
func TestDiscoveryRejectsMalformedOrUnfilteredLogsWithoutProgress(t *testing.T) {
	cases := map[string]func(map[string]any){
		"removed": func(l map[string]any) { l["removed"] = true }, "missing_removed": func(l map[string]any) { delete(l, "removed") },
		"txhash": func(l map[string]any) { l["transactionHash"] = "invalid" }, "wrong_contract": func(l map[string]any) { l["address"] = recipient },
		"outside_block": func(l map[string]any) { l["blockNumber"] = "0x63" }, "data": func(l map[string]any) { l["data"] = "0x1" },
		"recipient": func(l map[string]any) { l["topics"].([]string)[2] = "0x" + strings.Repeat("0", 64) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, e := discoveryFixture(t, mutate).Discover(context.Background(), discoverInput())
			if e == nil || p != nil {
				t.Fatal(p, e)
			}
		})
	}
}
func TestDiscoveryRejectsCursorBeforeInvoice(t *testing.T) {
	r := discoverInput()
	r.Cursor = "99"
	p, e := NewClient(BSC, nil).Discover(context.Background(), r)
	if p != nil || e == nil {
		t.Fatal(p, e)
	}
}

func TestDiscoveryBoundsChunkAndRejectsTruncatedResponses(t *testing.T) {
	for _, mode := range []string{"chunk", "null", "full", "wrong_chain"} {
		t.Run(mode, func(t *testing.T) {
			c := NewClient(BSC, &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
				var req struct {
					ID     uint64
					Method string
					Params []json.RawMessage
				}
				json.NewDecoder(r.Body).Decode(&req)
				var result any
				switch req.Method {
				case "eth_chainId":
					result = "0x38"
					if mode == "wrong_chain" {
						result = "0x1"
					}
				case "eth_getBlockByNumber":
					result = map[string]any{"number": "0x3e8", "hash": blockHash, "timestamp": "0x6553f164"}
				case "eth_getLogs":
					var filter map[string]any
					json.Unmarshal(req.Params[0], &filter)
					if filter["fromBlock"] != "0x64" || filter["toBlock"] != "0xc7" {
						t.Fatal(filter)
					}
					result = []any{}
					if mode == "null" {
						result = nil
					}
					if mode == "full" {
						logs := make([]any, 1000)
						for i := range logs {
							logs[i] = receiptFixture()["logs"].([]any)[0]
						}
						result = logs
					}
				default:
					t.Fatal(req.Method)
				}
				raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})})
			p, e := c.Discover(context.Background(), discoverInput())
			if mode == "chunk" {
				if e != nil || p.NextCursor != "200" || p.Done {
					t.Fatal(p, e)
				}
			} else if e == nil || p != nil {
				t.Fatal(p, e)
			}
		})
	}
}
func TestDiscoveryClosedEndpoints(t *testing.T) {
	for network, endpoint := range map[Network]string{BSC: "https://bsc-rpc.publicnode.com", Ethereum: "https://eth.drpc.org", Polygon: "https://polygon.drpc.org"} {
		c := NewDiscoveryClient(network)
		if c.spec.Endpoint != endpoint || c.http.Timeout <= 0 || c.http.Timeout > 10*time.Second || c.http.CheckRedirect == nil {
			t.Fatal(network, c)
		}
	}
}
