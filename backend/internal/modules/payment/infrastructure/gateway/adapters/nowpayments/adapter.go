// Package nowpayments implements the published NOWPayments API and IPN protocol.
package nowpayments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	payment "github.com/dujiao-next/internal/modules/payment/contract"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"io"
	"net/http"
	"strings"
	"time"
)

type Adapter struct {
	Client  *http.Client
	BaseURL string
}

func New() *Adapter {
	return &Adapter{Client: &http.Client{Timeout: 15 * time.Second}, BaseURL: "https://api.nowpayments.io/v1"}
}
func (a *Adapter) Type() string            { return "nowpayments:" }
func text(c jsonmap.JSON, k string) string { v, _ := c[k].(string); return v }
func (a *Adapter) ValidateConfig(c jsonmap.JSON, _ string) error {
	if text(c, "api_key") == "" || len(text(c, "ipn_secret")) < 20 || !strings.HasPrefix(text(c, "ipn_callback_url"), "https://") {
		return errors.New("NOWPayments API key, IPN secret and https IPN URL required")
	}
	return nil
}
func (a *Adapter) request(ctx context.Context, c jsonmap.JSON, path string, b any) (map[string]any, error) {
	var data []byte
	if b != nil {
		data, _ = json.Marshal(b)
	}
	method := "GET"
	if b != nil {
		method = "POST"
	}
	r, e := http.NewRequestWithContext(ctx, method, a.BaseURL+path, bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	r.Header.Set("x-api-key", text(c, "api_key"))
	r.Header.Set("Content-Type", "application/json")
	res, e := a.Client.Do(r)
	if e != nil {
		return nil, errors.New("NOWPayments unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return nil, errors.New("NOWPayments rejected request")
	}
	d := json.NewDecoder(io.LimitReader(res.Body, 1<<20))
	d.UseNumber()
	var out map[string]any
	e = d.Decode(&out)
	return out, e
}
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	default:
		return ""
	}
}
func amount(v any) (decimal.Decimal, error) {
	s := str(v)
	if s == "" {
		return decimal.Zero, errors.New("missing payment amount")
	}
	d, e := decimal.NewFromString(s)
	if e != nil || d.IsNegative() || d.Exponent() < -8 {
		return decimal.Zero, errors.New("invalid payment amount")
	}
	return d, nil
}
func (a *Adapter) CreatePayment(ctx context.Context, c jsonmap.JSON, in payment.GatewayCreateInput) (*payment.GatewayCreateResult, error) {
	if e := a.ValidateConfig(c, ""); e != nil {
		return nil, e
	}
	if in.Currency != "USDT" {
		return nil, errors.New("this store requires USDT wallet currency")
	}
	requested := in.Amount.Decimal.StringFixed(2)
	p, e := a.request(ctx, c, "/payment", map[string]any{"price_amount": requested, "price_currency": "usd", "pay_amount": requested, "pay_currency": "usdttrc20", "order_id": in.OrderNo, "ipn_callback_url": text(c, "ipn_callback_url"), "is_fixed_rate": true, "is_fee_paid_by_user": true})
	if e != nil {
		return nil, e
	}
	paid, e := amount(p["pay_amount"])
	if e != nil || paid.LessThan(in.Amount.Decimal) || str(p["payment_id"]) == "" || str(p["order_id"]) != in.OrderNo || str(p["pay_currency"]) != "usdttrc20" {
		return nil, errors.New("invalid NOWPayments quote")
	}
	p["wallet_credit_usdt"] = requested
	p["fee_policy"] = "payer covers provider fee; wallet credits requested USDT only"
	return &payment.GatewayCreateResult{ProviderRef: str(p["payment_id"]), QRCodeURL: str(p["pay_address"]), Payload: jsonmap.JSON(p), AmountSent: paid.StringFixed(2), CurrencySent: "USDT", DisplayChannelType: "USDT · TRON"}, nil
}
func (a *Adapter) result(p map[string]any) (*payment.GatewayCallbackResult, error) {
	pay, e := amount(p["pay_amount"])
	if e != nil {
		return nil, e
	}
	actual, e := amount(p["actually_paid"])
	if e != nil {
		return nil, e
	}
	if str(p["pay_currency"]) != "usdttrc20" || str(p["payment_id"]) == "" || str(p["order_id"]) == "" || pay.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("invalid NOWPayments payment")
	}
	status := "pending"
	if str(p["payment_status"]) == "finished" {
		if actual.LessThan(pay) {
			return nil, errors.New("underpayment")
		}
		status = "success"
	}
	now := time.Now()
	return &payment.GatewayCallbackResult{OrderNo: str(p["order_id"]), ProviderRef: str(p["payment_id"]), Status: status, Amount: money.FromDecimal(pay), Currency: "USDT", PaidAt: &now, Payload: jsonmap.JSON(p)}, nil
}
func (a *Adapter) QueryPayment(ctx context.Context, c jsonmap.JSON, ref string) (*payment.GatewayQueryResult, error) {
	if ref == "" || strings.ContainsAny(ref, "/?#") {
		return nil, errors.New("invalid payment reference")
	}
	p, e := a.request(ctx, c, "/payment/"+ref, nil)
	if e != nil {
		return nil, e
	}
	r, e := a.result(p)
	if e != nil {
		return nil, e
	}
	if r.ProviderRef != ref {
		return nil, errors.New("payment mismatch")
	}
	return &payment.GatewayQueryResult{ProviderRef: r.ProviderRef, Status: r.Status, Amount: r.Amount, Currency: r.Currency, PaidAt: r.PaidAt, Payload: r.Payload}, nil
}
func (a *Adapter) ParseWebhook(ctx context.Context, c jsonmap.JSON, headers map[string]string, body []byte, _ time.Time) (*payment.GatewayCallbackResult, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var payload map[string]any
	if d.Decode(&payload) != nil {
		return nil, errors.New("invalid IPN")
	}
	canonical, _ := json.Marshal(payload)
	mac := hmac.New(sha512.New, []byte(text(c, "ipn_secret")))
	mac.Write(canonical)
	sig := ""
	for k, v := range headers {
		if strings.EqualFold(k, "x-nowpayments-sig") {
			sig = v
		}
	}
	given, e := hex.DecodeString(sig)
	if e != nil || !hmac.Equal(mac.Sum(nil), given) {
		return nil, errors.New("invalid IPN signature")
	}
	ref := str(payload["payment_id"])
	if ref == "" || strings.ContainsAny(ref, "/?#") {
		return nil, errors.New("invalid payment reference")
	}
	fresh, e := a.request(ctx, c, "/payment/"+ref, nil)
	if e != nil {
		return nil, e
	}
	if str(fresh["payment_id"]) != ref || str(fresh["order_id"]) != str(payload["order_id"]) {
		return nil, fmt.Errorf("IPN payment mismatch")
	}
	return a.result(fresh)
}
