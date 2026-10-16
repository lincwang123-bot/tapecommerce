package upstream

import (
	"context"
	"encoding/json"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSupplyPurchaseCarriesCapAndCannotRedirectCredentials(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	var redirect atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect.Load() {
			http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
			return
		}
		var body CreateUpstreamOrderReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.MaxTotal != "8.00" || body.DownstreamOrderNo != "supply-test-1" {
			t.Errorf("wrong purchase: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"order_id":1,"status":"accepted"}`))
	}))
	defer source.Close()
	adapter := NewDujiaoNextAdapter(&siteconnectiondomain.Connection{BaseURL: source.URL, ApiKey: "synthetic-key", ApiSecret: "synthetic-secret"}, t.TempDir())
	request := CreateUpstreamOrderReq{SKUID: 21, Quantity: 2, MaxTotal: "8.00", DownstreamOrderNo: "supply-test-1"}
	if _, err := adapter.CreateOrder(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	redirect.Store(true)
	if _, err := adapter.CreateOrder(context.Background(), request); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() != 0 {
		t.Fatal("credentials or purchase sent to redirected supplier")
	}
}
