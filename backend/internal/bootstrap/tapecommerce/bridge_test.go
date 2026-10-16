package tapecommercebootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	carddomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	paymentapp "github.com/dujiao-next/internal/modules/payment/application"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymentstore "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	tapestore "github.com/dujiao-next/internal/modules/tapecommerce/infrastructure/gormstore"
)

func bh(c string) string { return "0x" + strings.Repeat(c, 64) }
func ba(c string) string { return "0x" + strings.Repeat(c, 40) }

var demoSignature = "0x" + strings.Repeat("a", 128) + "1b"

// Chain evidence is synthetic here; everything from original OrderService
// creation/reservation through payment/stock/outbox/claim uses real SQLite and
// original repositories/use cases. EVM parsing has separate local HTTP tests.
type safeDemoVerifier struct{}

func (safeDemoVerifier) Verify(_ context.Context, i domain.Invoice, hash string) (domain.Evidence, error) {
	return domain.Evidence{ChainID: 196, TxHash: hash, LogIndex: 0, BlockNumber: 10, BlockHash: bh("b"), SafeBlock: 11, Safe: true, PaidAt: i.FXSnapshot.IssuedAtMS / 1000}, nil
}

func newBridgeFixture(t *testing.T) (*Demo, uint, domain.Invoice) {
	t.Helper()
	t.Setenv("FACTORY_SHOP_ID", "")
	t.Setenv("VOUCHER_SERVICE_URL", "")
	t.Setenv("VOUCHER_DELIVERY_KEY", "synthetic-test-encryption-key-at-least-32")
	d, err := OpenDemo(t.TempDir(), Bridge{Enabled: true, GuestSecret: "synthetic-test-guest-secret-at-least-32", Checkout: ba("1"), Merchant: ba("2"), TenantHash: bh("3"), Verifier: safeDemoVerifier{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		root := d.Root
		if e := d.Close(); e != nil {
			t.Error(e)
		}
		if _, e := os.Stat(root); !os.IsNotExist(e) {
			t.Error("owned temp DB not removed", e)
		}
	})
	buyer, err := d.NewBuyer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q, err := d.CreateOrder(context.Background(), buyer, d.ProductID, ba("4"))
	if err != nil {
		t.Fatal("create original pending order:", err)
	}
	now := uint64(time.Now().UnixMilli())
	fx := domain.FXSnapshot{PolicyVersion: domain.FXPolicyVersion, Source: domain.FXSource, InstrumentID: domain.FXInstrument, PriceField: domain.FXPriceField, RateUSDTPerOKB: "117.91", SourceTimestampMS: now, FetchedAtMS: now, AmountUSDT: q.AmountUSDT, Rounding: domain.FXRounding, IssuedAtMS: now}
	wei, err := fx.QuoteAmountWei()
	if err != nil {
		t.Fatal(err)
	}
	i := domain.Invoice{InvoiceID: bh("5"), ShopID: DemoShopID, OrderID: q.OrderID, ChainID: 196, Checkout: d.Bridge.Checkout, TenantHash: d.Bridge.TenantHash, Payer: q.Payer, Merchant: d.Bridge.Merchant, AmountWei: wei, Deadline: now/1000 + 60, FXSnapshot: fx}
	return d, buyer, i
}
func persistFixture(t *testing.T, d *Demo, buyer uint, i domain.Invoice) QuoteResult {
	t.Helper()
	q, e := d.Bridge.Persist(context.Background(), buyer, i, demoSignature)
	if e != nil {
		t.Fatal(e)
	}
	return q
}
func counts(t *testing.T, d *Demo) (int64, int64) {
	t.Helper()
	var used, fulfilled int64
	if e := d.DB.Model(&carddomain.Secret{}).Where("status = ?", carddomain.StatusUsed).Count(&used).Error; e != nil {
		t.Fatal(e)
	}
	if e := d.DB.Model(&fulfillmentdomain.Fulfillment{}).Count(&fulfilled).Error; e != nil {
		t.Fatal(e)
	}
	return used, fulfilled
}

func TestOriginalUnpaidOrderAtomicPaymentStockDelivery(t *testing.T) {
	d, buyer, i := newBridgeFixture(t)
	before, e := orderstore.New(d.DB, d.Bridge.GuestSecret).GetByID(i.OrderID)
	if e != nil {
		t.Fatal(e)
	}
	if before.Status != constants.OrderStatusPendingPayment || before.PaidAt != nil || len(before.Children) != 1 {
		t.Fatalf("not original unpaid family: %+v", before)
	}
	q := persistFixture(t, d, buyer, i)
	if _, e := d.Bridge.ClaimDelivery(context.Background(), buyer, i.InvoiceID); !errors.Is(e, domain.ErrReview) {
		t.Fatal("early claim", e)
	}
	result, e := d.Bridge.Submit(context.Background(), buyer, i.InvoiceID, bh("6"))
	if e != nil || result.State != domain.Delivered {
		t.Fatalf("submit=%+v err=%v", result, e)
	}
	var p paymentdomain.Payment
	if e := d.DB.First(&p, q.PaymentID).Error; e != nil {
		t.Fatal(e)
	}
	if p.Status != constants.PaymentStatusSuccess || p.Amount.String() != "1.00" || p.Currency != "USDT" || p.ProviderRef != bh("6") || p.PaidAt == nil {
		t.Fatalf("ledger=%+v", p)
	}
	after, e := orderstore.New(d.DB, d.Bridge.GuestSecret).GetByID(i.OrderID)
	if e != nil {
		t.Fatal(e)
	}
	if after.Status != constants.OrderStatusCompleted || after.Children[0].Status != constants.OrderStatusCompleted {
		t.Fatalf("order status=%s child=%s", after.Status, after.Children[0].Status)
	}
	claim, e := d.Bridge.ClaimDelivery(context.Background(), buyer, i.InvoiceID)
	if e != nil || !strings.HasPrefix(claim.Payload, "TAPE-LAUNCH-KIT-DEMO-") {
		t.Fatal(claim, e)
	}
	var raw string
	if e := d.DB.Raw("SELECT payload FROM fulfillments LIMIT 1").Scan(&raw).Error; e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(raw, "dv1:") {
		t.Fatal("delivery is not encrypted at rest")
	}
	if used, fulfilled := counts(t, d); used != 1 || fulfilled != 1 {
		t.Fatal(used, fulfilled)
	}
	for n := 0; n < 3; n++ {
		if _, e := d.Bridge.Submit(context.Background(), buyer, i.InvoiceID, bh("6")); e != nil {
			t.Fatal(e)
		}
	}
	if used, fulfilled := counts(t, d); used != 1 || fulfilled != 1 {
		t.Fatal("replay", used, fulfilled)
	}
}

func TestAtomicBridgeConcurrentReplayFromPendingOrder(t *testing.T) {
	d, buyer, i := newBridgeFixture(t)
	persistFixture(t, d, buyer, i)
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := d.Bridge.Submit(context.Background(), buyer, i.InvoiceID, bh("6"))
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	result, e := d.Bridge.Status(context.Background(), buyer, i.InvoiceID)
	if e != nil || result.State != domain.Delivered {
		t.Fatal(result, e)
	}
	if used, fulfilled := counts(t, d); used != 1 || fulfilled != 1 {
		t.Fatal(used, fulfilled)
	}
}

func TestBridgeScopeAmountAndHistoricalQuoteGuards(t *testing.T) {
	d, buyer, i := newBridgeFixture(t)
	if _, e := d.Bridge.Prepare(context.Background(), buyer+1, i.OrderID, i.Payer); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("buyer scope", e)
	}
	wrong := i
	wrong.Payer = ba("7")
	if _, e := d.Bridge.Persist(context.Background(), buyer, wrong, demoSignature); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("payer pin", e)
	}
	wrong = i
	wrong.FXSnapshot.AmountUSDT = "2.00"
	wrong.AmountWei, _ = wrong.FXSnapshot.QuoteAmountWei()
	if _, e := d.Bridge.Persist(context.Background(), buyer, wrong, demoSignature); !errors.Is(e, domain.ErrReview) {
		t.Fatal("DB amount binding", e)
	}
	q := persistFixture(t, d, buyer, i)
	if again, e := d.Bridge.Persist(context.Background(), buyer, i, demoSignature); e != nil || again.PaymentID != q.PaymentID {
		t.Fatal("idempotent persist", again, e)
	}
	wrong = i
	wrong.InvoiceID = bh("8")
	if _, e := d.Bridge.Persist(context.Background(), buyer, wrong, demoSignature); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("history overwrite", e)
	}
	if _, e := d.Bridge.Submit(context.Background(), buyer+1, i.InvoiceID, bh("6")); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("submit buyer", e)
	}
	if _, e := d.Bridge.Status(context.Background(), buyer+1, i.InvoiceID); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("status buyer", e)
	}
	if _, e := d.Bridge.ClaimDelivery(context.Background(), buyer+1, i.InvoiceID); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("claim buyer", e)
	}
	if used, fulfilled := counts(t, d); used != 0 || fulfilled != 0 {
		t.Fatal(used, fulfilled)
	}
}

func TestAdmissionRollsBackPaymentAndStockOnStockout(t *testing.T) {
	d, buyer, i := newBridgeFixture(t)
	q := persistFixture(t, d, buyer, i)
	if e := d.DB.Where("1=1").Delete(&carddomain.Secret{}).Error; e != nil {
		t.Fatal(e)
	}
	status, e := d.Bridge.Submit(context.Background(), buyer, i.InvoiceID, bh("6"))
	if e != nil || status.State != domain.ReviewRequired {
		t.Fatal(status, e)
	}
	var p paymentdomain.Payment
	if e := d.DB.First(&p, q.PaymentID).Error; e != nil {
		t.Fatal(e)
	}
	if p.Status != constants.PaymentStatusPending || p.PaidAt != nil {
		t.Fatal("partial ledger commit", p.Status)
	}
	var o orderdomain.Order
	if e := d.DB.First(&o, i.OrderID).Error; e != nil {
		t.Fatal(e)
	}
	if o.Status != constants.OrderStatusPendingPayment || o.PaidAt != nil {
		t.Fatal("partial paid order", o.Status)
	}
	if used, fulfilled := counts(t, d); used != 0 || fulfilled != 0 {
		t.Fatal(used, fulfilled)
	}
}

func TestReviewAfterClaimBlocksRealStockAndPaidLedger(t *testing.T) {
	d, buyer, i := newBridgeFixture(t)
	q := persistFixture(t, d, buyer, i)
	ctx := context.Background()
	ts := tapestore.New(d.DB)
	if e := d.Bridge.service().Submit(ctx, i.InvoiceID, bh("6")); e != nil {
		t.Fatal(e)
	}
	if _, e := d.Bridge.service().Check(ctx, i.InvoiceID); e != nil {
		t.Fatal(e)
	}
	j, e := ts.Claim(ctx, DemoShopID, i.InvoiceID, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e := ts.Review(ctx, DemoShopID, i.InvoiceID, "test_review"); e != nil {
		t.Fatal(e)
	}
	if e := d.Bridge.AdmitAndFulfill(ctx, j); !errors.Is(e, domain.ErrReview) {
		t.Fatal(e)
	}
	var p paymentdomain.Payment
	if e := d.DB.First(&p, q.PaymentID).Error; e != nil {
		t.Fatal(e)
	}
	if p.Status != constants.PaymentStatusPending {
		t.Fatal(p.Status)
	}
	if used, fulfilled := counts(t, d); used != 0 || fulfilled != 0 {
		t.Fatal(used, fulfilled)
	}
}

func TestBridgeRejectsCanceledRefundedPaidAndChangedOrders(t *testing.T) {
	for _, change := range []string{"canceled", "refunded", "already_paid", "amount_changed", "child_canceled", "payment_expired"} {
		t.Run(change, func(t *testing.T) {
			d, buyer, i := newBridgeFixture(t)
			q := persistFixture(t, d, buyer, i)
			var e error
			switch change {
			case "canceled":
				_, e = d.Orders.CancelOrder(i.OrderID, buyer)
			case "refunded":
				e = d.DB.Model(&orderdomain.Order{}).Where("id = ?", i.OrderID).Update("refunded_amount", "0.01").Error
			case "already_paid":
				e = d.DB.Model(&orderdomain.Order{}).Where("id = ?", i.OrderID).Updates(map[string]any{"status": constants.OrderStatusPaid, "paid_at": time.Now()}).Error
			case "amount_changed":
				e = d.DB.Model(&orderdomain.Order{}).Where("id = ?", i.OrderID).Update("total_amount", "2.00").Error
			case "child_canceled":
				e = d.DB.Model(&orderdomain.Order{}).Where("parent_id = ?", i.OrderID).Update("status", constants.OrderStatusCanceled).Error
			case "payment_expired":
				e = d.DB.Model(&paymentdomain.Payment{}).Where("id = ?", q.PaymentID).Update("status", constants.PaymentStatusExpired).Error
			}
			if e != nil {
				t.Fatal(e)
			}
			status, e := d.Bridge.Submit(context.Background(), buyer, i.InvoiceID, bh("6"))
			if e != nil || status.State != domain.ReviewRequired {
				t.Fatal(status, e)
			}
			if used, fulfilled := counts(t, d); used != 0 || fulfilled != 0 {
				t.Fatal(used, fulfilled)
			}
			var p paymentdomain.Payment
			if e := d.DB.First(&p, q.PaymentID).Error; e != nil {
				t.Fatal(e)
			}
			if p.Status == constants.PaymentStatusSuccess {
				t.Fatal("unsafe settlement")
			}
		})
	}
}

func TestGenericCallbackCannotAdmitTapeCommercePayment(t *testing.T) {
	d, buyer, i := newBridgeFixture(t)
	q := persistFixture(t, d, buyer, i)
	svc := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{PaymentStore: paymentstore.New(d.DB, d.Bridge.GuestSecret)})
	if _, e := svc.HandleCallback(paymentapp.PaymentCallbackInput{PaymentID: q.PaymentID, Status: constants.PaymentStatusSuccess, ProviderRef: bh("6")}); !errors.Is(e, paymentapp.ErrPaymentInvalid) {
		t.Fatal("generic callback bypass", e)
	}
	if used, fulfilled := counts(t, d); used != 0 || fulfilled != 0 {
		t.Fatal(used, fulfilled)
	}
}

func TestDemoHTTPRequiresServerSecretAndBuyerScope(t *testing.T) {
	d, buyer, i := newBridgeFixture(t)
	persistFixture(t, d, buyer, i)
	secret := strings.Repeat("s", 32)
	handler := DemoHandler(d, secret)
	request := func(path string, body any, auth, origin string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", auth)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if r := request("/v1/invoices/claim", map[string]any{"buyerId": buyer, "invoiceId": i.InvoiceID}, "", ""); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if r := request("/v1/invoices/claim", map[string]any{"buyerId": buyer, "invoiceId": i.InvoiceID}, "Bearer "+secret, "https://browser.invalid"); r.Code != 403 {
		t.Fatal(r.Code)
	}
	if r := request("/v1/invoices/status", map[string]any{"buyerId": buyer + 1, "invoiceId": i.InvoiceID}, "Bearer "+secret, ""); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := request("/v1/invoices/status", map[string]any{"buyerId": buyer, "invoiceId": i.InvoiceID, "amountWei": "1"}, "Bearer "+secret, ""); r.Code != 400 {
		t.Fatal(r.Code)
	}
	r := request("/v1/invoices/submit", map[string]any{"buyerId": buyer, "invoiceId": i.InvoiceID, "txHash": bh("6")}, "Bearer "+secret, "")
	if r.Code != 200 || strings.Contains(r.Body.String(), "TAPE-LAUNCH-KIT-DEMO-") {
		t.Fatal("status leaked payload", r.Code, r.Body.String())
	}
	r = request("/v1/invoices/claim", map[string]any{"buyerId": buyer, "invoiceId": i.InvoiceID}, "Bearer "+secret, "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "TAPE-LAUNCH-KIT-DEMO-") {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestBridgeDefaultDisabledAndAdmissionRequiresRealTransaction(t *testing.T) {
	if _, e := (&Bridge{}).Prepare(context.Background(), 1, 1, ba("1")); !errors.Is(e, domain.ErrDisabled) {
		t.Fatal(e)
	}
	d, _, _ := newBridgeFixture(t)
	if _, e := tapestore.New(d.DB).LockAdmission(context.Background(), domain.Job{}, time.Now()); !errors.Is(e, domain.ErrDisabled) {
		t.Fatal("non-transaction admission", e)
	}
}

func TestDemoLifetimeBudgetSurvivesClientSessionEviction(t *testing.T) {
	d, buyer, i := newBridgeFixture(t)
	if e := d.DB.Model(&demoBudget{}).Where("key IN ?", []string{"buyers", "orders"}).Update("count", 1000).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := d.NewBuyer(context.Background()); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("unbounded buyer creation", e)
	}
	if _, e := d.CreateOrder(context.Background(), buyer, d.ProductID, i.Payer); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("unbounded order creation", e)
	}
}
