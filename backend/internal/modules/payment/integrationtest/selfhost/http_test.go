package selfhost_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/dujiao-next/internal/app/container"
	"github.com/dujiao-next/internal/app/httpserver/middleware"
	paymentbootstrap "github.com/dujiao-next/internal/bootstrap/payment"
	walletbootstrap "github.com/dujiao-next/internal/bootstrap/wallet"
	"github.com/dujiao-next/internal/constants"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	app "github.com/dujiao-next/internal/modules/payment/application"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	store "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	paymenthttp "github.com/dujiao-next/internal/modules/payment/transport/http"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	walletstore "github.com/dujiao-next/internal/modules/wallet/infrastructure/gormstore"
	wallethttp "github.com/dujiao-next/internal/modules/wallet/transport/http"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"net/http/httptest"
	"strings"
	"testing"
)

func httpFixture(t *testing.T, f *fixture) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	orders := orderstore.New(f.db, "synthetic-guest-secret-only")
	wallets := walletstore.New(f.db)
	c := &container.Container{OrderService: orderapp.NewOrderService(orderapp.OrderServiceOptions{OrderStore: orders}), OrderStore: orders, PaymentService: f.service, PaymentStore: store.New(f.db, "synthetic-guest-secret-only"), PaymentChannelStore: store.NewChannelStore(f.db), WalletRepo: wallets, WalletService: walletapp.NewService(walletapp.Options{Repository: wallets, Transactions: wallets}), UserStore: userstore.New(f.db)}
	p := paymentbootstrap.New(c)
	w := walletbootstrap.New(c)
	r := gin.New()
	r.Use(middleware.CommerceLaunchGate())
	// Authentication is injected only in this isolated router. Production uses
	// its existing JWT middleware; ownership and Guest credentials remain real.
	r.Use(func(c *gin.Context) {
		if c.GetHeader("X-Fixture-User") == "owner" {
			c.Set("user_id", f.user.ID)
		}
		if c.GetHeader("X-Fixture-User") == "foreign" {
			c.Set("user_id", f.user.ID+100)
		}
		c.Next()
	})
	member := r.Group("/api/v1")
	guest := r.Group("/api/v1/guest")
	paymenthttp.RegisterUserWriteRoutes(member, p.Write)
	paymenthttp.RegisterGuestWriteRoutes(guest, p.Write)
	paymenthttp.RegisterUserLatestRoute(member, p.Latest)
	paymenthttp.RegisterGuestLatestRoute(guest, p.Latest)
	wallethttp.RegisterUserRoutes(member, w.User)
	return r
}
func request(t *testing.T, r *gin.Engine, method, path, body, user, guest string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fixture-User", user)
	if guest != "" {
		req.Header.Set("Authorization", "Guest "+base64.RawURLEncoding.EncodeToString([]byte(guest)))
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var data map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatalf("non-JSON %d %s", w.Code, w.Body.String())
	}
	return w.Code, data
}
func TestClosedHTTPRejectsForeignOwnersAndAllowsExistingHashVerification(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "false")
	f := setup(t)
	p := f.order(t)
	r := httpFixture(t, f)
	path := fmt.Sprintf("/api/v1/payments/%d/submit-tx", p.ID)
	body := `{"transaction_hash":"0x` + strings.Repeat("a", 64) + `"}`
	for _, user := range []string{"", "foreign"} {
		_, v := request(t, r, "POST", path, body, user, "")
		if v["status_code"] == float64(0) {
			t.Fatal("foreign or missing user authorized")
		}
	}
	for _, bad := range []string{`{"transaction_hash":"a","network":"bsc"}`, `{} {}`, `{"transaction_hash":"` + strings.Repeat("x", 3000) + `"}`} {
		_, v := request(t, r, "POST", path, bad, "owner", "")
		if v["status_code"] == float64(0) {
			t.Fatal("invalid request accepted")
		}
	}
	_, v := request(t, r, "POST", path, body, "owner", "")
	if v["status_code"] != float64(0) || v["data"].(map[string]any)["status"] != "success" {
		t.Fatal(v)
	}
	for _, path := range []string{"/api/v1/payments", "/api/v1/payments/1/capture", "/api/v1/wallet/recharge", "/api/v1/guest/payments"} {
		status, _ := request(t, r, "POST", path, `{}`, "owner", "")
		if status != 503 {
			t.Fatalf("closed checkout opened %s: %d", path, status)
		}
	}
	// A non-selfhost provider can never be charged through this gate exception.
	f.db.Model(&domain.Payment{}).Where("id=?", p.ID).Update("provider_type", "paypal")
	_, v = request(t, r, "POST", fmt.Sprintf("/api/v1/payments/%d/submit-tx", p.ID), body, "owner", "")
	if v["status_code"] == float64(0) {
		t.Fatal("legacy capture provider accepted")
	}
}
func TestGuestHTTPUsesStoredOrderPasswordAndRestoresClosedInvoice(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "false")
	f := setup(t)
	repo := orderstore.New(f.db, "synthetic-guest-secret-only")
	o := &orderdomain.Order{OrderNo: "guest-http-fixture", GuestEmail: "guest@example.test", GuestPassword: "synthetic-order-password", Status: constants.OrderStatusPendingPayment, Currency: "USDT", TotalAmount: money.FromDecimal(decimal.NewFromInt(20))}
	if e := repo.Create(o, nil); e != nil {
		t.Fatal(e)
	}
	result, e := f.service.CreatePayment(app.CreatePaymentInput{OrderID: o.ID, ChannelID: f.channel.ID})
	if e != nil {
		t.Fatal(e)
	}
	p := result.Payment
	r := httpFixture(t, f)
	path := fmt.Sprintf("/api/v1/guest/payments/%d/submit-tx", p.ID)
	body := `{"transaction_hash":"0x` + strings.Repeat("b", 64) + `"}`
	for _, creds := range []string{"", "guest@example.test\nwrong", "foreign@example.test\nsynthetic-order-password"} {
		_, v := request(t, r, "POST", path, body, "", creds)
		if v["status_code"] == float64(0) {
			t.Fatal("wrong Guest credentials authorized")
		}
	}
	_, v := request(t, r, "POST", path+"?email=guest@example.test&password=synthetic-order-password", body, "", "")
	if v["status_code"] == float64(0) {
		t.Fatal("URL password authorized")
	}
	f.db.Model(&orderdomain.Order{}).Where("id=?", o.ID).Update("status", "canceled")
	creds := "guest@example.test\nsynthetic-order-password"
	_, v = request(t, r, "GET", "/api/v1/guest/payments/latest?order_no="+o.OrderNo, "", "", creds)
	if v["status_code"] != float64(0) || v["data"].(map[string]any)["can_transfer"] != false {
		t.Fatal("closed invoice not restored safely", v)
	}
	_, v = request(t, r, "POST", path, body, "", creds)
	if v["status_code"] != float64(0) || v["data"].(map[string]any)["verification_status"] != "review_required" {
		t.Fatal("closed guest payment not held for review", v)
	}
	assertUnpaid(t, f, p)
}
func TestRechargeHTTPChecksRealRechargeOwnership(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "false")
	f := setup(t)
	p, e := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(25)), Currency: "USDT"})
	if e != nil {
		t.Fatal(e)
	}
	r := httpFixture(t, f)
	path := fmt.Sprintf("/api/v1/wallet/recharge/payments/%d/submit-tx", p.Payment.ID)
	body := `{"transaction_hash":"0x` + strings.Repeat("c", 64) + `"}`
	for _, user := range []string{"", "foreign"} {
		_, v := request(t, r, "POST", path, body, user, "")
		if v["status_code"] == float64(0) {
			t.Fatal("foreign recharge authorized")
		}
	}
	_, v := request(t, r, "POST", path, body, "owner", "")
	if v["status_code"] != float64(0) || v["data"].(map[string]any)["status"] != "success" {
		t.Fatal(v)
	}
}
