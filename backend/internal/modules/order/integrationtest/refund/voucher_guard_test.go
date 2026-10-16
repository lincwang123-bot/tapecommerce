package refund_test

import (
	"encoding/json"
	card "github.com/dujiao-next/internal/modules/cardsecret/domain"
	user "github.com/dujiao-next/internal/modules/identity/user/domain"
	. "github.com/dujiao-next/internal/modules/order/application/refund"
	order "github.com/dujiao-next/internal/modules/order/domain"
	wallet "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefundBlockedUntilVoucherVoidAndNoDoubleCredit(t *testing.T) {
	var allow atomic.Bool
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var b struct {
			Refs []string `json:"external_refs"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		if len(b.Refs) != 1 || b.Refs[0] != "SALE-REFUND" {
			t.Error("wrong refund refs", b)
		}
		if !allow.Load() {
			w.WriteHeader(409)
			return
		}
		w.Write([]byte(`{"voided":true}`))
	}))
	defer server.Close()
	t.Setenv("VOUCHER_SERVICE_URL", server.URL)
	t.Setenv("VOUCHER_API_SECRET", "test-refund-secret-long-enough-32chars")
	svc, db := setupOrderRefundWalletTest(t)
	now := time.Now()
	u := user.User{Email: "refund@example.com", PasswordHash: "unused", Status: "active"}
	db.Create(&u)
	amt := money.FromDecimal(decimal.NewFromInt(20))
	o := order.Order{OrderNo: "SALE-REFUND", UserID: u.ID, Status: "delivered", TotalAmount: amt, WalletPaidAmount: amt, Currency: "USDT", PaidAt: &now, CreatedAt: now}
	db.Create(&o)
	input := AdminRefundToWalletInput{OrderID: o.ID, Amount: amt}
	if _, _, _, e := svc.AdminRefundToWallet(input); e == nil {
		t.Fatal("refund accepted without revocation")
	}
	var n int64
	db.Model(&wallet.Transaction{}).Count(&n)
	if n != 0 {
		t.Fatal("credited blocked refund")
	}
	allow.Store(true)
	if _, _, _, e := svc.AdminRefundToWallet(input); e != nil {
		t.Fatal(e)
	}
	_, _, _, _ = svc.AdminRefundToWallet(input)
	db.Model(&wallet.Transaction{}).Count(&n)
	if n != 1 {
		t.Fatal("duplicate credit", n)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected remote attempts", calls.Load())
	}
}

func TestNativeStockRefundNeverRevokesUpstreamOrRecyclesSoldCards(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	t.Setenv("VOUCHER_SERVICE_URL", server.URL)
	t.Setenv("VOUCHER_DELIVERY_KEY", "isolated-native-refund-key")
	svc, db := setupOrderRefundWalletTest(t)
	sql, _ := db.DB()
	defer sql.Close()
	if e := db.AutoMigrate(&card.Secret{}); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	u := user.User{Email: "native-refund@example.test", Status: "active"}
	db.Create(&u)
	amount := money.FromDecimal(decimal.NewFromInt(20))
	o := order.Order{OrderNo: "NATIVE-REFUND", UserID: u.ID, Status: "completed", TotalAmount: amount, WalletPaidAmount: amount, Currency: "USDT", PaidAt: &now, CreatedAt: now}
	db.Create(&o)
	if err := db.Create(&order.OrderItem{OrderID: o.ID, ProductID: 77, TitleJSON: jsonmap.JSON{"zh-CN": "native"}, Quantity: 1, FulfillmentType: "auto"}).Error; err != nil {
		t.Fatal(err)
	}
	db.Create(&card.Secret{ProductID: 77, Secret: "ALREADY-SOLD", Status: "used", OrderID: &o.ID, UsedAt: &now})
	// Native stock permits the normal partial refund contract.
	if _, _, _, e := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: o.ID, Amount: money.FromDecimal(decimal.NewFromInt(10))}); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 0 {
		t.Fatal("native refund contacted voucher service")
	}
	var used int64
	db.Model(&card.Secret{}).Where("status = ?", "used").Count(&used)
	if used != 1 {
		t.Fatal("sold card recycled")
	}
}
