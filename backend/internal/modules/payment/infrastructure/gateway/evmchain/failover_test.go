package evmchain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFailoverRechecksWholeProofAndBoundsAttempts(t *testing.T) {
	t.Setenv("SELFHOST_RPC_FAILOVER_ENABLED", "true")
	for _, mode := range []string{"success", "wrong_chain", "wrong_amount", "pending", "outage"} {
		t.Run(mode, func(t *testing.T) {
			var primary, backup int
			c := NewClient(BSC, &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == Endpoint {
					primary++
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if r.URL.String() != "https://bsc-rpc.publicnode.com" {
					t.Fatal("unexpected provider", r.URL)
				}
				backup++
				if mode == "outage" {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				var req struct {
					ID     uint64
					Method string
				}
				json.NewDecoder(r.Body).Decode(&req)
				var result any
				switch req.Method {
				case "eth_chainId":
					result = "0x38"
					if mode == "wrong_chain" {
						result = "0x1"
					}
				case "eth_getTransactionReceipt":
					result = receiptFixture()
					if mode == "pending" {
						result = nil
					}
				case "eth_getBlockByNumber":
					result = map[string]any{"number": "0x64", "hash": blockHash, "timestamp": "0x6553f164"}
				default:
					t.Fatal(req.Method)
				}
				raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})})
			input := expectation()
			if mode == "wrong_amount" {
				input.AmountUnits = "2"
			}
			result, err := c.Verify(context.Background(), input)
			if primary != 1 || backup < 1 || backup > 4 {
				t.Fatalf("unbounded calls %d %d", primary, backup)
			}
			if mode == "success" {
				if err != nil || result == nil || backup != 4 {
					t.Fatal(result, err, backup)
				}
			} else if err == nil || result != nil {
				t.Fatal("accepted invalid backup evidence", result, err)
			}
		})
	}
}

func TestFailoverDefaultOffAndNoRetryForPending(t *testing.T) {
	t.Setenv("SELFHOST_RPC_FAILOVER_ENABLED", "")
	if NewClient(BSC, nil).backupEndpoint != "" {
		t.Fatal("must be opt in")
	}
	t.Setenv("SELFHOST_RPC_FAILOVER_ENABLED", "true")
	c := fixtureClient(t, func(method string, _ []json.RawMessage, result any) any {
		if method == "eth_getTransactionReceipt" {
			return nil
		}
		return result
	})
	if _, err := c.Verify(context.Background(), expectation()); err == nil || err.Kind != "pending" {
		t.Fatal(err)
	}
}
