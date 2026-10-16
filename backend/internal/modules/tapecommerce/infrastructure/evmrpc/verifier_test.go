package evmrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
)

func hash(c string) string        { return "0x" + strings.Repeat(c, 64) }
func address(c string) string     { return "0x" + strings.Repeat(c, 40) }
func topic(address string) string { return "0x" + strings.Repeat("0", 24) + address[2:] }
func fixtureInvoice() domain.Invoice {
	return domain.Invoice{InvoiceID: hash("1"), ShopID: "shop-1", OrderID: 1, ChainID: 196, Checkout: address("2"), TenantHash: hash("3"), Payer: address("4"), Merchant: address("5"), AmountWei: "1000000000000000000", Deadline: 5000, FXSnapshot: domain.FXSnapshot{PolicyVersion: domain.FXPolicyVersion, Source: domain.FXSource, InstrumentID: domain.FXInstrument, PriceField: domain.FXPriceField, RateUSDTPerOKB: "100", AmountUSDT: "100.00", SourceTimestampMS: 4940000, FetchedAtMS: 4940000, IssuedAtMS: 4940000, Rounding: domain.FXRounding}}
}
func fixtureReceipt() receipt {
	i := fixtureInvoice()
	return receipt{TransactionHash: hash("6"), Status: "0x1", To: i.Checkout, From: i.Payer, BlockNumber: "0x64", BlockHash: hash("7"), Logs: []logEntry{{Address: i.Checkout, Topics: []string{EventTopic, i.InvoiceID, topic(i.Payer), topic(i.Merchant)}, Data: "0x" + fmt.Sprintf("%064x", uint64(1000000000000000000)) + i.TenantHash[2:], LogIndex: "0x2", TransactionHash: hash("6"), BlockHash: hash("7"), BlockNumber: "0x64"}}}
}

type fixture struct {
	receipt     receipt
	chain       string
	safe        string
	blockHash   string
	timestamp   string
	unavailable bool
	disappear   bool
}

func normalFixture() fixture {
	return fixture{receipt: fixtureReceipt(), chain: "0xc4", safe: "0x65", blockHash: hash("7"), timestamp: "0x1360"}
}

// Test RPC servers are local-only and are authoritatively closed by t.Cleanup.
func server(t *testing.T, f fixture) *Client {
	t.Helper()
	var receipts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.unavailable {
			w.WriteHeader(503)
			return
		}
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("bad test RPC request")
			w.WriteHeader(400)
			return
		}
		var result any
		switch req.Method {
		case "eth_chainId":
			result = f.chain
		case "eth_getTransactionReceipt":
			if !f.disappear || receipts.Add(1) == 1 {
				result = f.receipt
			}
		case "eth_getBlockByNumber":
			var number string
			_ = json.Unmarshal(req.Params[0], &number)
			if number == "safe" {
				number = f.safe
			}
			result = block{Number: number, Hash: f.blockHash, Timestamp: f.timestamp}
		default:
			t.Errorf("unexpected RPC: %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestVerifySafeHTTPReceipts(t *testing.T) {
	v := Verifier{Primary: server(t, normalFixture()), Witness: server(t, normalFixture())}
	e, err := v.Verify(context.Background(), fixtureInvoice(), hash("6"))
	if err != nil || !e.Safe || e.LogIndex != 2 || e.SafeBlock != 101 {
		t.Fatalf("evidence=%+v err=%v", e, err)
	}
}

func TestRejectInvalidChainPaymentFacts(t *testing.T) {
	tests := []struct {
		name   string
		change func(*fixture)
		want   error
	}{
		{"wrong_chain", func(f *fixture) { f.chain = "0x38" }, domain.ErrReceipt},
		{"failed_transaction", func(f *fixture) { f.receipt.Status = "0x0" }, domain.ErrReceipt},
		{"wrong_contract", func(f *fixture) { f.receipt.To = address("a") }, domain.ErrReceipt},
		{"wrong_sender", func(f *fixture) { f.receipt.From = address("a") }, domain.ErrReceipt},
		{"wrong_event_address", func(f *fixture) { f.receipt.Logs[0].Address = address("a") }, domain.ErrReceipt},
		{"wrong_invoice", func(f *fixture) { f.receipt.Logs[0].Topics[1] = hash("a") }, domain.ErrReceipt},
		{"wrong_payer", func(f *fixture) { f.receipt.Logs[0].Topics[2] = topic(address("a")) }, domain.ErrReceipt},
		{"wrong_merchant", func(f *fixture) { f.receipt.Logs[0].Topics[3] = topic(address("a")) }, domain.ErrReceipt},
		{"wrong_amount", func(f *fixture) {
			f.receipt.Logs[0].Data = "0x" + fmt.Sprintf("%064x", 100) + fixtureInvoice().TenantHash[2:]
		}, domain.ErrReceipt},
		{"wrong_tenant", func(f *fixture) { f.receipt.Logs[0].Data = f.receipt.Logs[0].Data[:66] + hash("a")[2:] }, domain.ErrReceipt},
		{"duplicate_event", func(f *fixture) { f.receipt.Logs = append(f.receipt.Logs, f.receipt.Logs[0]) }, domain.ErrReceipt},
		{"removed_event", func(f *fixture) { f.receipt.Logs[0].Removed = true }, domain.ErrReceipt},
		{"before_quote_issue", func(f *fixture) { f.timestamp = "0x100" }, domain.ErrReceipt},
		{"late_block", func(f *fixture) { f.timestamp = "0xffff" }, domain.ErrReceipt},
		{"reorg", func(f *fixture) { f.blockHash = hash("a") }, domain.ErrReorg},
		{"rpc_unavailable", func(f *fixture) { f.unavailable = true }, domain.ErrRPC},
		{"receipt_disappears", func(f *fixture) { f.disappear = true }, domain.ErrReorg},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := normalFixture()
			tt.change(&f)
			v := Verifier{Primary: server(t, f), Witness: server(t, normalFixture())}
			_, err := v.Verify(context.Background(), fixtureInvoice(), hash("6"))
			if !errors.Is(err, tt.want) {
				t.Fatalf("want %v got %v", tt.want, err)
			}
		})
	}
}

func TestSafeLagAndWitnessDisagreementFailClosed(t *testing.T) {
	t.Run("safe_lag", func(t *testing.T) {
		f := normalFixture()
		f.safe = "0x63"
		v := Verifier{Primary: server(t, normalFixture()), Witness: server(t, f)}
		e, err := v.Verify(context.Background(), fixtureInvoice(), hash("6"))
		if err != nil || e.Safe {
			t.Fatalf("unsafe evidence=%+v err=%v", e, err)
		}
	})
	t.Run("witness_fork", func(t *testing.T) {
		f := normalFixture()
		f.blockHash = hash("a")
		f.receipt.BlockHash = f.blockHash
		f.receipt.Logs[0].BlockHash = f.blockHash
		v := Verifier{Primary: server(t, normalFixture()), Witness: server(t, f)}
		_, err := v.Verify(context.Background(), fixtureInvoice(), hash("6"))
		if !errors.Is(err, domain.ErrReorg) {
			t.Fatalf("want disagreement: %v", err)
		}
	})
	t.Run("witness_timestamp_disagreement", func(t *testing.T) {
		f := normalFixture()
		f.timestamp = "0x1361"
		_, err := (Verifier{Primary: server(t, normalFixture()), Witness: server(t, f)}).Verify(context.Background(), fixtureInvoice(), hash("6"))
		if !errors.Is(err, domain.ErrReorg) {
			t.Fatal("timestamp disagreement accepted", err)
		}
	})
	t.Run("missing_witness", func(t *testing.T) {
		v := Verifier{Primary: server(t, normalFixture())}
		_, err := v.Verify(context.Background(), fixtureInvoice(), hash("6"))
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatal(err)
		}
	})
}

func TestClientRejectsResponseIDMismatch(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":999,"result":"0xc4"}`))
	}))
	defer s.Close()
	c, _ := NewClient(s.URL)
	var chain string
	if err := c.Call(context.Background(), "eth_chainId", []any{}, &chain); !errors.Is(err, domain.ErrRPC) {
		t.Fatal(err)
	}
}
