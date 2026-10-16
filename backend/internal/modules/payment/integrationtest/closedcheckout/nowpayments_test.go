// Cross-layer integration belongs here: real gate + transport + application +
// adapter + SQLite. All gateway I/O is a RoundTripper; no listener/network exists.
package closedcheckout_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/app/httpserver/middleware"
	"github.com/dujiao-next/internal/constants"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	productstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	paymentapp "github.com/dujiao-next/internal/modules/payment/application"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/adapters/nowpayments"
	"github.com/dujiao-next/internal/modules/payment/infrastructure/gateway/provider"
	paymentstore "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	paymenttransport "github.com/dujiao-next/internal/modules/payment/transport/http"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	settingsstore "github.com/dujiao-next/internal/modules/settings/infrastructure/gormstore"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	walletstore "github.com/dujiao-next/internal/modules/wallet/infrastructure/gormstore"
	"github.com/dujiao-next/internal/platform/database/gormdb"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type captureNowTransport func(*http.Request) (*http.Response, error)

func (f captureNowTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// DTO translation only; the production service executes every webhook operation.
type webhookBridge struct{ svc *paymentapp.PaymentService }

func callback(in paymenttransport.WebhookCallbackInput) paymentapp.WebhookCallbackInput {
	return paymentapp.WebhookCallbackInput{ChannelID: in.ChannelID, Headers: in.Headers, Body: in.Body, Context: in.Context}
}
func (b webhookBridge) HandleNowPaymentsWebhook(in paymenttransport.WebhookCallbackInput) (*paymentdomain.Payment, string, error) {
	return b.svc.HandleNowPaymentsWebhook(callback(in))
}
func (b webhookBridge) HandlePaypalWebhook(in paymenttransport.WebhookCallbackInput) (*paymentdomain.Payment, string, error) {
	return b.svc.HandlePaypalWebhook(callback(in))
}
func (b webhookBridge) HandleStripeWebhook(in paymenttransport.WebhookCallbackInput) (*paymentdomain.Payment, string, error) {
	return b.svc.HandleStripeWebhook(callback(in))
}
func (b webhookBridge) HandleDujiaoPayWebhook(in paymenttransport.WebhookCallbackInput) (*paymentdomain.Payment, string, error) {
	return b.svc.HandleDujiaoPayWebhook(callback(in))
}

// Same concrete store/service wiring as setupPaymentServiceWalletTest, adapted
// here because application white-box tests cannot import Gin/transport layers.
func setup(t *testing.T, adapter *nowpayments.Adapter) (*paymentapp.PaymentService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "isolated.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err = db.AutoMigrate(&userdomain.User{}, &orderdomain.Order{}, &orderdomain.OrderItem{}, &fulfillmentdomain.Fulfillment{}, &productdomain.Product{}, &productdomain.ProductSKU{}, &walletdomain.Account{}, &walletdomain.Transaction{}, &walletdomain.RechargeOrder{}, &paymentdomain.PaymentChannel{}, &paymentdomain.Payment{}, &settingsstore.SettingRecord{}); err != nil {
		t.Fatal(err)
	}
	oldDB := gormdb.DB
	gormdb.DB = db
	t.Cleanup(func() { gormdb.DB = oldDB })
	walletRepo := walletstore.New(db)
	registry := provider.NewRegistry()
	registry.Register("nowpayments", "", adapter)
	return paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{OrderStore: orderstore.New(db, "isolated-fixture-secret-32-bytes-only"), ProductRepo: productstore.NewProductStore(db), ProductSKURepo: productstore.NewSKUStore(db), PaymentStore: paymentstore.New(db, "isolated-fixture-secret-32-bytes-only"), ChannelStore: paymentstore.NewChannelStore(db), WalletRepo: walletRepo, UserStore: userstore.New(db), WalletService: walletapp.NewService(walletapp.Options{Repository: walletRepo, Transactions: walletRepo}), SettingService: settingsapp.NewService(settingsstore.New(db)), ExpireMinutes: 15, PaymentProviderRegistry: registry}), db
}

func TestClosedCheckoutExistingRechargeWebhookVerifiedExactlyOnce(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "false")
	salesFile := filepath.Join(t.TempDir(), "sales.json")
	if err := os.WriteFile(salesFile, []byte(`{"enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_SALES_FILE", salesFile)
	gin.SetMode(gin.TestMode)
	const ipnSecret = "synthetic-ipn-secret-at-least-20-bytes"
	freshState, actual := "confirming", "0.00"
	queries := 0
	adapter := nowpayments.New()
	adapter.Client = &http.Client{Transport: captureNowTransport(func(r *http.Request) (*http.Response, error) {
		queries++
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "api.nowpayments.io" || !strings.HasPrefix(r.URL.Path, "/v1/payment/") {
			t.Fatalf("unexpected outgoing request: %s %s", r.Method, r.URL.Path)
		}
		ref := strings.TrimPrefix(r.URL.Path, "/v1/payment/")
		order := "WR-CLOSED-ONLY"
		if ref == "unknown-payment" {
			order = "UNKNOWN-ORDER"
		}
		raw, _ := json.Marshal(map[string]string{"payment_id": ref, "order_id": order, "payment_status": freshState, "pay_amount": "88.00", "actually_paid": actual, "pay_currency": "usdttrc20"})
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})}
	svc, db := setup(t, adapter)
	amount := money.FromDecimal(decimal.NewFromInt(88))
	payment := &paymentdomain.Payment{ChannelID: 1, ProviderType: "nowpayments", ChannelType: "usdttrc20", InteractionMode: "qr", Amount: amount, Currency: "USDT", Status: constants.PaymentStatusPending, ProviderRef: "1001", GatewayOrderNo: "WR-CLOSED-ONLY"}
	recharge := &walletdomain.RechargeOrder{RechargeNo: "WR-CLOSED-ONLY", UserID: 1, ChannelID: 1, ProviderType: "nowpayments", ChannelType: "usdttrc20", InteractionMode: "qr", Amount: amount, PayableAmount: amount, Currency: "USDT", Status: constants.WalletRechargeStatusPending}
	if err := db.Create(payment).Error; err != nil {
		t.Fatal(err)
	}
	recharge.PaymentID = payment.ID
	if err := db.Create(recharge).Error; err != nil {
		t.Fatal(err)
	}
	channel := &paymentdomain.PaymentChannel{ID: 1, Name: "isolated-only", ProviderType: "nowpayments", ChannelType: "usdttrc20", InteractionMode: "qr", IsActive: true, ConfigJSON: jsonmap.JSON{"api_key": "mock-only", "ipn_secret": ipnSecret, "ipn_callback_url": "https://rehearsal.invalid/ipn"}}
	if err := db.Create(channel).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(middleware.CommerceLaunchGate())
	handler := paymenttransport.NewWebhookHandler(webhookBridge{svc}, nil)
	router.POST("/api/v1/payments/webhook/nowpayments", handler.NowPaymentsWebhook)
	for _, path := range []string{"/api/v1/orders", "/api/v1/orders/create-and-pay", "/api/v1/wallet/recharge"} {
		router.POST(path, func(c *gin.Context) { t.Error("closed checkout creation reached handler"); c.Status(200) })
	}
	post := func(ref, order string, valid bool) int {
		// Notification always claims finished; only the fresh mock query is authoritative.
		raw, _ := json.Marshal(map[string]string{"payment_id": ref, "order_id": order, "payment_status": "finished", "pay_amount": "88.00", "actually_paid": "88.00", "pay_currency": "usdttrc20"})
		mac := hmac.New(sha512.New, []byte(ipnSecret))
		mac.Write(raw)
		signature := hex.EncodeToString(mac.Sum(nil))
		if !valid {
			signature = "00"
		}
		request := httptest.NewRequest("POST", "/api/v1/payments/webhook/nowpayments?channel_id=1", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("x-nowpayments-sig", signature)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Code
	}
	ledgerCount := func() int64 {
		var n int64
		if err := db.Model(&walletdomain.Transaction{}).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	if code := post("1001", "WR-CLOSED-ONLY", false); code != 400 || queries != 0 || ledgerCount() != 0 {
		t.Fatalf("invalid signature: status=%d queries=%d", code, queries)
	}
	if code := post("1001", "WR-CLOSED-ONLY", true); code != 200 || queries != 1 || ledgerCount() != 0 {
		t.Fatalf("pending query credited or blocked: status=%d queries=%d", code, queries)
	}
	freshState, actual = "finished", "87.99"
	if code := post("1001", "WR-CLOSED-ONLY", true); code != 400 || queries != 2 || ledgerCount() != 0 {
		t.Fatalf("underpaid query accepted: status=%d queries=%d", code, queries)
	}
	actual = "88.00"
	for i := 0; i < 5; i++ {
		if code := post("1001", "WR-CLOSED-ONLY", true); code != 200 {
			t.Fatalf("verified existing recharge blocked: %d", code)
		}
	}
	if ledgerCount() != 1 {
		t.Fatal("replayed webhook credited more than once")
	}
	var wallet walletdomain.Account
	if err := db.Where("user_id = ?", 1).First(&wallet).Error; err != nil {
		t.Fatal(err)
	}
	if !wallet.Balance.Decimal.Equal(decimal.NewFromInt(88)) {
		t.Fatal("incorrect wallet credit")
	}
	if err := db.First(payment, payment.ID).Error; err != nil {
		t.Fatal(err)
	}
	if payment.Status != constants.PaymentStatusSuccess {
		t.Fatal("payment not successful")
	}
	if err := db.First(recharge, recharge.ID).Error; err != nil {
		t.Fatal(err)
	}
	if recharge.Status != constants.WalletRechargeStatusSuccess {
		t.Fatal("recharge not successful")
	}
	if code := post("unknown-payment", "UNKNOWN-ORDER", true); code != 200 {
		t.Fatalf("unknown valid webhook did not receive safe acknowledgement: %d", code)
	}
	for _, model := range []any{&paymentdomain.Payment{}, &walletdomain.RechargeOrder{}, &walletdomain.Transaction{}} {
		var n int64
		db.Model(model).Count(&n)
		if n != 1 {
			t.Fatal("unknown webhook created a business row")
		}
	}
	for _, path := range []string{"/api/v1/orders", "/api/v1/orders/create-and-pay", "/api/v1/wallet/recharge"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if rec.Code != 503 {
			t.Fatalf("new checkout not blocked: %s %d", path, rec.Code)
		}
	}
	t.Logf("verified closed gate with real webhook/service/adapter: gateway_queries=%d ledger_rows=1 credit=88.00 USDT; no network sockets", queries)
}
