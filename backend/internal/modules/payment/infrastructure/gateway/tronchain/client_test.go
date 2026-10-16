package tronchain

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

const testTx = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testHash = "0000000000000064bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func requestFixture() Request {
	return Request{TxID: testTx, Recipient: USDTContract, AmountUnits: "1000000", EarliestPaidAt: time.Unix(1700000000, 0), MinBlockNumber: 99}
}
func receiptFixture() map[string]any {
	return map[string]any{"id": testTx, "blockNumber": 100, "blockTimeStamp": 1700000100000, "receipt": map[string]any{"result": "SUCCESS"}, "log": []any{map[string]any{"address": contractHex, "topics": []string{transferTopic, strings.Repeat("0", 24) + strings.Repeat("2", 40), strings.Repeat("0", 24) + contractHex}, "data": strings.Repeat("0", 59) + "f4240"}}}
}
func fixture(t *testing.T, mutate func(string, map[string]any) map[string]any) *Client {
	return NewClient(&http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.trongrid.io" {
			t.Fatal("target")
		}
		value := map[string]any{"blockID": testHash, "block_header": map[string]any{"raw_data": map[string]any{"number": 100, "timestamp": 1700000100000}}, "transactions": []any{map[string]any{"txID": testTx}}}
		if strings.HasSuffix(r.URL.Path, "gettransactioninfobyid") {
			value = receiptFixture()
		}
		if mutate != nil {
			value = mutate(r.URL.Path, value)
		}
		b, _ := json.Marshal(value)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})})
}
func TestAddressOfficialGoldenVector(t *testing.T) {
	raw, ok := decodeAddress(USDTContract)
	if !ok || hexString(raw) != "41"+contractHex {
		t.Fatal("bad golden address")
	}
	if encodeAddress(raw) != USDTContract {
		t.Fatal("roundtrip")
	}
	for _, a := range []string{"", USDTContract + "1", "0x" + contractHex, USDTContract[:33] + "0"} {
		if _, ok := NormalizeAddress(a); ok {
			t.Fatal("accepted invalid address")
		}
	}
}
func TestSolidifiedTransferAndHead(t *testing.T) {
	c := fixture(t, nil)
	r, e := c.Verify(context.Background(), requestFixture())
	if e != nil {
		t.Fatal(e)
	}
	if r.LogIndex != 0 || r.AmountUnits != "1000000" || r.BlockHash != testHash {
		t.Fatalf("%+v", r)
	}
	h, e := c.ReadHead(context.Background())
	if e != nil || h.Number != 100 {
		t.Fatal(h, e)
	}
}

func TestResourceFeesDoNotChangeRequiredTokenTransfer(t *testing.T) {
	for _, fee := range []int{0, 100000000} {
		for _, short := range []bool{false, true} {
			c := fixture(t, func(path string, v map[string]any) map[string]any {
				if strings.HasSuffix(path, "gettransactioninfobyid") {
					v["fee"] = fee
					v["receipt"].(map[string]any)["energy_fee"] = fee
					if short {
						v["log"].([]any)[0].(map[string]any)["data"] = strings.Repeat("0", 59) + "f423f"
					}
				}
				return v
			})
			result, err := c.Verify(context.Background(), requestFixture())
			if short {
				if result != nil || err == nil || err.Code != "transfer_not_matched" {
					t.Fatalf("resource fee must not excuse a short transfer: fee=%d result=%+v err=%v", fee, result, err)
				}
			} else if err != nil || result.AmountUnits != requestFixture().AmountUnits {
				t.Fatalf("resource fee changed token accounting: fee=%d result=%+v err=%v", fee, result, err)
			}
		}
	}
}
func TestRejectWrongTronEvidence(t *testing.T) {
	for _, scenario := range []string{"pending", "failed", "fake", "recipient", "amount", "old", "same_block", "ambiguous", "missing_receipt", "bad_padding", "canonical_tx", "canonical_time", "future_head"} {
		t.Run(scenario, func(t *testing.T) {
			c := fixture(t, func(path string, v map[string]any) map[string]any {
				if strings.HasSuffix(path, "gettransactioninfobyid") {
					if scenario == "pending" {
						return map[string]any{}
					}
					log := v["log"].([]any)[0].(map[string]any)
					switch scenario {
					case "failed":
						v["receipt"] = map[string]any{"result": "REVERT"}
					case "missing_receipt":
						delete(v, "receipt")
					case "fake":
						log["address"] = strings.Repeat("1", 40)
					case "recipient":
						log["topics"].([]string)[2] = strings.Repeat("0", 24) + strings.Repeat("1", 40)
					case "amount":
						log["data"] = strings.Repeat("f", 64)
					case "ambiguous":
						v["log"] = []any{log, log}
					case "bad_padding":
						log["topics"].([]string)[2] = "1" + strings.Repeat("0", 23) + contractHex
					}
				} else if strings.HasSuffix(path, "getblockbynum") {
					if scenario == "canonical_tx" {
						v["transactions"] = []any{}
					}
					if scenario == "canonical_time" {
						v["block_header"].(map[string]any)["raw_data"].(map[string]any)["timestamp"] = 1
					}
				} else if scenario == "future_head" {
					v["block_header"].(map[string]any)["raw_data"].(map[string]any)["number"] = 99
				}
				return v
			})
			req := requestFixture()
			if scenario == "old" {
				req.EarliestPaidAt = time.Unix(1700000200, 0)
			}
			if scenario == "same_block" {
				req.MinBlockNumber = 100
			}
			if _, err := c.Verify(context.Background(), req); err == nil {
				t.Fatal("bad evidence accepted")
			}
		})
	}
}

func TestTronTransportAndInvalidInputFailClosed(t *testing.T) {
	calls := 0
	c := NewClient(&http.Client{Timeout: time.Hour, Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("PRIVATE_RPC_SECRET"))}, nil
	})})
	if c.http.Timeout > 10*time.Second || c.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("transport policy")
	}
	req := requestFixture()
	req.AmountUnits = "1.0"
	if _, e := c.Verify(context.Background(), req); e == nil || e.Kind != "invalid_input" || calls != 0 {
		t.Fatal("invalid input hit network", e)
	}
	if _, e := c.Verify(context.Background(), requestFixture()); e == nil || e.Kind != "unavailable" || strings.Contains(e.Error(), "PRIVATE") {
		t.Fatal(e)
	}
	for _, payload := range []string{"null", "invalid JSON", strings.Repeat("x", maxResponseBytes+1)} {
		c := NewClient(&http.Client{Transport: mockTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
		})})
		if _, e := c.Verify(context.Background(), requestFixture()); e == nil || e.Kind != "unavailable" {
			t.Fatal(e)
		}
	}
}
