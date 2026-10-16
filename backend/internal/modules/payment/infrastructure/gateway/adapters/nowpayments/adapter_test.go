package nowpayments

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIPNRequiresSignatureFreshLookupAndFullAmount(t *testing.T) {
	p := map[string]any{"payment_id": "123", "order_id": "ORDER1", "payment_status": "finished", "pay_currency": "usdttrc20", "pay_amount": "20.000001", "actually_paid": "20.000001"}
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; json.NewEncoder(w).Encode(p) }))
	defer s.Close()
	a := New()
	a.BaseURL = s.URL
	c := jsonmap.JSON{"api_key": "test", "ipn_secret": strings.Repeat("s", 32)}
	b, _ := json.Marshal(p)
	mac := hmac.New(sha512.New, []byte(text(c, "ipn_secret")))
	mac.Write(b)
	sig := hex.EncodeToString(mac.Sum(nil))
	headers := map[string]string{"x-nowpayments-sig": sig}
	if _, e := a.ParseWebhook(context.Background(), c, map[string]string{}, b, time.Now()); e == nil || calls != 0 {
		t.Fatal("unsigned callback accepted or queried")
	}
	r, e := a.ParseWebhook(context.Background(), c, headers, b, time.Now())
	if e != nil || r.Status != "success" || calls != 1 {
		t.Fatal(r, e, calls)
	}
	p["actually_paid"] = "19"
	if _, e := a.ParseWebhook(context.Background(), c, headers, b, time.Now()); e == nil {
		t.Fatal("underpayment accepted")
	}
	delete(p, "actually_paid")
	if _, e := a.ParseWebhook(context.Background(), c, headers, b, time.Now()); e == nil {
		t.Fatal("missing amount accepted")
	}
	p["actually_paid"] = "25"
	p["pay_currency"] = "btc"
	if _, e := a.ParseWebhook(context.Background(), c, headers, b, time.Now()); e == nil {
		t.Fatal("wrong coin accepted")
	}
}
