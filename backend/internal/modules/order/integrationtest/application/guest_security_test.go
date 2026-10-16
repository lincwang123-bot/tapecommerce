package application_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dujiao-next/internal/constants"
	coupongorm "github.com/dujiao-next/internal/modules/coupon/infrastructure/gormstore"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderhttp "github.com/dujiao-next/internal/modules/order/transport/http"
	reseller "github.com/dujiao-next/internal/modules/reseller/contract"
	"github.com/gin-gonic/gin"
)

func guestFixture(t *testing.T) (couponLimitRaceFixture, *orderapp.OrderService, orderapp.CreateGuestOrderInput) {
	f := newCouponLimitRaceFixture(t, "guest_secure", 0, 0)
	if e := f.db.Model(&f.product).Update("purchase_type", constants.ProductPurchaseGuest).Error; e != nil {
		t.Fatal(e)
	}
	svc := f.service(coupongorm.New(f.db), coupongorm.NewUsageStore(f.db))
	return f, svc, orderapp.CreateGuestOrderInput{Email: "guest@example.test", OrderPassword: "synthetic-guest-password", ClientRequestID: "guest-request-001", ExpectedTotal: "100.00", ExpectedCurrency: "CNY", Items: f.input(1).Items}
}
func TestGuestConcurrentReplayCredentialsAndQuote(t *testing.T) {
	f, svc, input := guestFixture(t)
	wrong := input
	wrong.ExpectedTotal = "99.00"
	if _, e := svc.CreateGuestOrder(wrong); e == nil || e.Error() != "quote_changed" {
		t.Fatal(e)
	}
	var count int64
	f.db.Model(&orderdomain.Order{}).Count(&count)
	if count != 0 {
		t.Fatal("bad quote persisted order")
	}
	var wg sync.WaitGroup
	results := make(chan *orderdomain.Order, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := svc.CreateGuestOrder(input)
			if e != nil {
				errs <- e
			} else {
				results <- o
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	var original *orderdomain.Order
	for o := range results {
		if original == nil {
			original = o
		} else if original.ID != o.ID {
			t.Fatal("duplicate guest order")
		}
	}
	f.db.Model(&orderdomain.Order{}).Where("parent_id IS NULL").Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
	if strings.Contains(original.OrderNo, input.Email) || strings.Contains(original.RequestHash, input.OrderPassword) {
		t.Fatal("raw guest credentials in idempotency fields")
	}
	wrong = input
	wrong.OrderPassword = "different-password"
	if _, e := svc.CreateGuestOrder(wrong); e == nil || e.Error() != "idempotency_conflict" {
		t.Fatal("changed password accepted", e)
	}
	wrong = input
	wrong.Items = append([]orderapp.CreateOrderItem{}, input.Items...)
	wrong.Items[0].Quantity = 2
	if _, e := svc.CreateGuestOrder(wrong); e == nil || e.Error() != "idempotency_conflict" {
		t.Fatal(e)
	}
	if e := f.db.Model(&f.sku).Update("price_amount", "150.00").Error; e != nil {
		t.Fatal(e)
	}
	restarted := f.service(coupongorm.New(f.db), coupongorm.NewUsageStore(f.db))
	replay, e := restarted.CreateGuestOrder(input)
	if e != nil || replay.ID != original.ID || replay.TotalAmount.String() != "100.00" {
		t.Fatal("old price changed", e)
	}
	wrong = input
	wrong.ClientRequestID = "new-order-after-price-change"
	if _, e := svc.CreateGuestOrder(wrong); e == nil || e.Error() != "quote_changed" {
		t.Fatal(e)
	}
}

type guestQueryAdapter struct{ *orderapp.OrderService }

func (a guestQueryAdapter) GetOrderByGuestOrderNoForTenant(tenant reseller.TenantContext, no, email, password string) (*orderdomain.Order, error) {
	o, e := a.OrderService.GetOrderByGuestOrderNoForTenant(tenant, no, email, password)
	if e != nil {
		return nil, orderhttp.ErrGuestOrderNotFound
	}
	return o, nil
}

type guestChannelPolicy struct {
	seen    int
	amount  string
	hasUser bool
}

func (p *guestChannelPolicy) GetAllowedChannelIDsForOrder([]orderdomain.OrderItem) []uint {
	return []uint{2}
}
func (p *guestChannelPolicy) GetAvailableChannels(f orderhttp.AvailablePaymentChannelFilter) ([]map[string]interface{}, error) {
	p.seen++
	p.amount = f.TargetAmount.String()
	p.hasUser = f.User != nil
	return []map[string]interface{}{{"id": uint(1)}, {"id": uint(2)}}, nil
}
func TestGuestPaymentChannelsUsesOrderOwnershipAndAmount(t *testing.T) {
	_, svc, input := guestFixture(t)
	order, e := svc.CreateGuestOrder(input)
	if e != nil {
		t.Fatal(e)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	policy := &guestChannelPolicy{}
	orderhttp.RegisterGuestReadRoutes(r.Group("/guest"), orderhttp.NewGuestHandler(guestQueryAdapter{svc}, policy, nil))
	for _, password := range []string{"wrong-password", input.OrderPassword} {
		body, _ := json.Marshal(map[string]any{"order_no": order.OrderNo, "amount": "0.01"})
		req := httptest.NewRequest("POST", "/guest/order/payment-channels", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Guest "+base64.RawURLEncoding.EncodeToString([]byte(input.Email+"\n"+password)))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var result struct {
			Status int              `json:"status_code"`
			Data   []map[string]any `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		if password != input.OrderPassword {
			if result.Status != 404 || policy.seen != 0 {
				t.Fatal("wrong credential leaked channels", w.Body.String())
			}
		} else if result.Status != 0 || len(result.Data) != 1 || result.Data[0]["id"] != float64(2) || policy.amount != "100.00" || policy.hasUser {
			t.Fatal("wrong channel policy", w.Body.String(), policy)
		}
	}
}

// The real HTTP DTO passes quote/key fields through to the SQLite application.
type guestCreateAdapter struct{ *orderapp.OrderService }

func (a guestCreateAdapter) CreateOrder(orderhttp.CreateOrderInput) (*orderdomain.Order, error) {
	panic("member path not expected")
}
func (a guestCreateAdapter) CreateGuestOrder(i orderhttp.CreateGuestOrderInput) (*orderdomain.Order, error) {
	items := make([]orderapp.CreateOrderItem, len(i.Items))
	for n, x := range i.Items {
		items[n] = orderapp.CreateOrderItem{ProductID: x.ProductID, SKUID: x.SKUID, Quantity: x.Quantity}
	}
	return a.OrderService.CreateGuestOrder(orderapp.CreateGuestOrderInput{Email: i.Email, OrderPassword: i.OrderPassword, Tenant: i.Tenant, Items: items, ClientRequestID: i.ClientRequestID, ExpectedTotal: i.ExpectedTotal, ExpectedCurrency: i.ExpectedCurrency, PaymentChannelID: i.PaymentChannelID})
}
func TestGuestHTTPCreateConfirmationAndReplay(t *testing.T) {
	f, svc, input := guestFixture(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := orderhttp.NewCreateHandler(guestCreateAdapter{svc}, nil, nil, nil)
	r.POST("/create", h.CreateGuestOrderAndPay)
	var first string
	for n, total := range []string{"99.00", "100.00", "100.00"} {
		body, _ := json.Marshal(map[string]any{"email": input.Email, "order_password": input.OrderPassword, "client_order_id": "http-guest-001", "expected_total": total, "expected_currency": "CNY", "items": []map[string]any{{"product_id": f.product.ID, "sku_id": f.sku.ID, "quantity": 1}}})
		req := httptest.NewRequest("POST", "/create", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out struct {
			Status int `json:"status_code"`
			Data   struct {
				Order struct {
					OrderNo string `json:"order_no"`
				} `json:"order"`
			} `json:"data"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			var count int64
			f.db.Model(&orderdomain.Order{}).Count(&count)
			if out.Status != 400 || count != 0 {
				t.Fatal("bad HTTP quote created order", out.Status, count)
			}
			continue
		}
		if out.Status != 0 || out.Data.Order.OrderNo == "" {
			t.Fatal("HTTP create failed", w.Body.String())
		}
		if first == "" {
			first = out.Data.Order.OrderNo
		} else if first != out.Data.Order.OrderNo {
			t.Fatal("HTTP duplicate order")
		}
	}
}

func TestGuestVoucherRequiresExplicitConfirmation(t *testing.T) {
	f, svc, input := guestFixture(t)
	t.Setenv("VOUCHER_SERVICE_URL", "http://127.0.0.1:1")
	for _, change := range []func(*orderapp.CreateGuestOrderInput){
		func(i *orderapp.CreateGuestOrderInput) { i.ClientRequestID = "" },
		func(i *orderapp.CreateGuestOrderInput) { i.ExpectedTotal = "" },
		func(i *orderapp.CreateGuestOrderInput) { i.ExpectedCurrency = "CNY" },
	} {
		candidate := input
		candidate.ExpectedCurrency = "USDT"
		change(&candidate)
		if _, e := svc.CreateGuestOrder(candidate); e == nil || e.Error() != "invalid_confirmation" {
			t.Fatal("missing confirmation accepted", e)
		}
	}
	var count int64
	f.db.Model(&orderdomain.Order{}).Count(&count)
	if count != 0 {
		t.Fatal("unconfirmed orders persisted")
	}
}
func TestGuestTenantCannotReplayMainOrder(t *testing.T) {
	_, svc, input := guestFixture(t)
	if _, e := svc.CreateGuestOrder(input); e != nil {
		t.Fatal(e)
	}
	input.Tenant = reseller.ResellerTenantContext("reseller.example.test", 7, 0, "")
	if order, e := svc.CreateGuestOrder(input); e == nil || order != nil {
		t.Fatal("tenant replayed another scope")
	}
	input.Tenant = reseller.UnavailableTenantContext("unknown.example.test", "unknown")
	if order, e := svc.CreateGuestOrder(input); e == nil || order != nil {
		t.Fatal("unavailable tenant replayed main scope")
	}
}
