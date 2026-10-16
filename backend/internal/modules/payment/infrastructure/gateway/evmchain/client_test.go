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

type mockTransport func(*http.Request) (*http.Response, error)

func (f mockTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const tx = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const blockHash = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const recipient = "0x1111111111111111111111111111111111111111"

func expectation() Request {
	return Request{MinBlockNumber: 99, TxID: tx, Recipient: recipient, AmountUnits: "1000000000000000000", EarliestPaidAt: time.Unix(1700000000, 0)}
}
func receiptFixture() map[string]any {
	return map[string]any{"transactionHash": tx, "blockHash": blockHash, "blockNumber": "0x64", "status": "0x1", "logs": []any{map[string]any{
		"address": USDTContract, "topics": []string{transferTopic, "0x" + strings.Repeat("0", 24) + strings.Repeat("2", 40), "0x" + strings.Repeat("0", 24) + recipient[2:]},
		"data": "0x" + strings.Repeat("0", 48) + "0de0b6b3a7640000", "logIndex": "0x2", "transactionHash": tx, "blockHash": blockHash, "blockNumber": "0x64", "removed": false,
	}}}
}
func fixtureClient(t *testing.T, mutate func(string, []json.RawMessage, any) any) *Client {
	t.Helper()
	seen := map[uint64]bool{}
	return NewClient(BSC, &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != Endpoint || r.Method != "POST" {
			t.Fatal("unexpected target")
		}
		var request struct {
			ID     uint64
			Method string
			Params []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if seen[request.ID] || request.ID == 0 {
			t.Fatal("RPC request id reused")
		}
		seen[request.ID] = true
		var result any
		switch request.Method {
		case "eth_chainId":
			result = "0x38"
		case "eth_getTransactionReceipt":
			result = receiptFixture()
		case "eth_getBlockByNumber":
			result = map[string]any{"number": "0x64", "hash": blockHash, "timestamp": "0x6553f164"}
		default:
			t.Fatal("unexpected RPC", request.Method)
		}
		if mutate != nil {
			result = mutate(request.Method, request.Params, result)
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})})
}
func TestVerifyExactFinalizedTransfer(t *testing.T) {
	request := expectation()
	request.TxID = strings.ToUpper(tx[2:])
	request.TxID = "0x" + request.TxID
	result, err := fixtureClient(t, nil).Verify(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.TxID != tx || result.LogIndex != 2 || result.AmountUnits != request.AmountUnits || result.BlockNumber != 100 || result.PaidAt.Unix() != 1700000100 {
		t.Fatalf("%+v", result)
	}
}

func TestGasFeesDoNotChangeRequiredTokenTransfer(t *testing.T) {
	for _, gasPrice := range []string{"0x1", "0x174876e800"} {
		for _, short := range []bool{false, true} {
			name := "exact"
			if short {
				name = "short_by_one_atomic_unit"
			}
			t.Run(gasPrice+"/"+name, func(t *testing.T) {
				client := fixtureClient(t, func(method string, _ []json.RawMessage, value any) any {
					if method == "eth_getTransactionReceipt" {
						rec := value.(map[string]any)
						rec["gasUsed"] = "0x11170"
						rec["effectiveGasPrice"] = gasPrice
						if short {
							log := rec["logs"].([]any)[0].(map[string]any)
							log["data"] = "0x" + strings.Repeat("0", 48) + "0de0b6b3a763ffff"
						}
					}
					return value
				})
				result, err := client.Verify(context.Background(), expectation())
				if short {
					if result != nil || err == nil || err.Code != "transfer_not_matched" {
						t.Fatalf("gas must not excuse a short token transfer: result=%+v err=%v", result, err)
					}
				} else if err != nil || result.AmountUnits != expectation().AmountUnits {
					t.Fatalf("native gas changed token accounting: result=%+v err=%v", result, err)
				}
			})
		}
	}
}
func TestRejectConfirmedMismatchesAndPending(t *testing.T) {
	for _, scenario := range []string{"not_found", "wrong_chain", "failed", "wrong_hash", "wrong_contract", "wrong_recipient", "wrong_amount", "old", "ambiguous", "removed", "bad_padding", "log_block", "canonical_reorg", "not_finalized", "huge_amount"} {
		t.Run(scenario, func(t *testing.T) {
			client := fixtureClient(t, func(method string, params []json.RawMessage, result any) any {
				if method == "eth_chainId" && scenario == "wrong_chain" {
					return "0x1"
				}
				if method == "eth_getTransactionReceipt" {
					if scenario == "not_found" {
						return nil
					}
					rec := result.(map[string]any)
					log := rec["logs"].([]any)[0].(map[string]any)
					switch scenario {
					case "failed":
						rec["status"] = "0x0"
					case "wrong_hash":
						rec["transactionHash"] = blockHash
					case "wrong_contract":
						log["address"] = recipient
					case "wrong_recipient":
						log["topics"].([]string)[2] = "0x" + strings.Repeat("0", 24) + strings.Repeat("3", 40)
					case "wrong_amount":
						log["data"] = "0x" + strings.Repeat("0", 63) + "1"
					case "huge_amount":
						log["data"] = "0x" + strings.Repeat("f", 64)
					case "ambiguous":
						other := map[string]any{}
						for k, v := range log {
							other[k] = v
						}
						other["logIndex"] = "0x3"
						rec["logs"] = []any{log, other}
					case "removed":
						log["removed"] = true
					case "bad_padding":
						log["topics"].([]string)[2] = "0x1" + strings.Repeat("0", 23) + recipient[2:]
					case "log_block":
						log["blockHash"] = tx
					}
				}
				if method == "eth_getBlockByNumber" {
					b := result.(map[string]any)
					if scenario == "old" {
						b["timestamp"] = "0x1"
					}
					if scenario == "canonical_reorg" && string(params[0]) != "\"finalized\"" {
						b["hash"] = tx
					}
					if scenario == "not_finalized" && string(params[0]) == "\"finalized\"" {
						b["number"] = "0x63"
					}
				}
				return result
			})
			_, err := client.Verify(context.Background(), expectation())
			if err == nil {
				t.Fatal("accepted mismatch")
			}
			if (scenario == "not_found" || scenario == "not_finalized") && err.Kind != "pending" {
				t.Fatal(err)
			}
		})
	}
}
func TestInvalidInputDoesNotCallRPC(t *testing.T) {
	c := NewClient(BSC, &http.Client{Transport: mockTransport(func(*http.Request) (*http.Response, error) { t.Fatal("network called"); return nil, nil })})
	for _, v := range []Request{
		{TxID: "bad"}, {TxID: tx, Recipient: recipient, AmountUnits: "1.2", EarliestPaidAt: time.Now()},
		{TxID: tx, Recipient: recipient, AmountUnits: "0", EarliestPaidAt: time.Now()},
		{TxID: tx, Recipient: "0x" + strings.Repeat("0", 40), AmountUnits: "1", EarliestPaidAt: time.Now()},
	} {
		if _, err := c.Verify(context.Background(), v); err == nil || err.Kind != "invalid_input" {
			t.Fatal(err)
		}
	}
}
func TestRPCFailuresAreSanitized(t *testing.T) {
	for _, payload := range []string{"not json", `{"jsonrpc":"2.0","id":999,"result":"0x38"}`, `{"jsonrpc":"2.0","id":1,"error":{"message":"SECRET"}}`, strings.Repeat("x", maxResponseBytes+1)} {
		c := NewClient(BSC, &http.Client{Transport: mockTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
		})})
		_, err := c.Verify(context.Background(), expectation())
		if err == nil || err.Kind != "unavailable" || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(err)
		}
	}
}

func TestInvoiceBlockBarrierAndReadHead(t *testing.T) {
	c := fixtureClient(t, nil)
	head, err := c.ReadHead(context.Background())
	if err != nil || head.Number != 100 || head.Hash != blockHash {
		t.Fatal(head, err)
	}
	request := expectation()
	for _, number := range []uint64{0, 100, 101} {
		request.MinBlockNumber = number
		if _, err := c.Verify(context.Background(), request); err == nil {
			t.Fatal("same/older invoice block accepted")
		}
	}
}
func TestContextAndRedirectSafety(t *testing.T) {
	client := &http.Client{Timeout: time.Hour, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	c := NewClient(BSC, client)
	if c.http.Timeout > 10*time.Second || c.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("unsafe transport policy")
	}
	if client.Timeout != time.Hour {
		t.Fatal("injected client mutated")
	}
	c.http.Transport = mockTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Verify(ctx, expectation()); err == nil || err.Kind != "unavailable" {
		t.Fatal(err)
	}
}

func TestAllEVMNetworksUseOwnTokenAndChainWithoutFallback(t *testing.T) {
	for _, network := range []Network{BSC, Ethereum, Polygon} {
		t.Run(string(network), func(t *testing.T) {
			spec, _ := Specification(network)
			for _, wrongChain := range []bool{false, true} {
				request := expectation()
				if network != BSC {
					request.AmountUnits = "1000000"
				}
				calls := 0
				c := NewClient(network, &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.String() != spec.Endpoint {
						t.Fatal("cross-network endpoint")
					}
					var q struct {
						ID     uint64
						Method string
					}
					_ = json.NewDecoder(r.Body).Decode(&q)
					var result any
					switch q.Method {
					case "eth_chainId":
						result = spec.chainHex
						if wrongChain {
							result = "0x61"
						}
					case "eth_getTransactionReceipt":
						rec := receiptFixture()
						log := rec["logs"].([]any)[0].(map[string]any)
						log["address"] = spec.TokenContract
						if network != BSC {
							log["data"] = "0x" + strings.Repeat("0", 59) + "f4240"
						}
						result = rec
					case "eth_getBlockByNumber":
						result = map[string]any{"number": "0x64", "hash": blockHash, "timestamp": "0x6553f164"}
					default:
						t.Fatal(q.Method)
					}
					raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result})
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
				})})
				result, e := c.Verify(context.Background(), request)
				if wrongChain {
					if e == nil || e.Code != "wrong_chain" || calls != 1 {
						t.Fatal("network fallback", e, calls)
					}
				} else if e != nil || result.AmountUnits != request.AmountUnits {
					t.Fatal(result, e)
				}
			}
		})
	}
	if _, ok := Specification(Network("bsc-testnet")); ok {
		t.Fatal("unknown chain accepted")
	}
}
