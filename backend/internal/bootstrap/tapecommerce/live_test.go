package tapecommercebootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	productstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	paymentapp "github.com/dujiao-next/internal/modules/payment/application"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymentstore "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	reseller "github.com/dujiao-next/internal/modules/reseller/contract"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	tapestore "github.com/dujiao-next/internal/modules/tapecommerce/infrastructure/gormstore"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type liveTestSigner struct {
	config                 config.TapeCommerceConfig
	healthCalls, signCalls atomic.Int64
	alter                  func(*SignedQuote)
	fail                   bool
}

func (s *liveTestSigner) Health(context.Context) (SignerHealth, error) {
	s.healthCalls.Add(1)
	if s.fail {
		return SignerHealth{}, ErrSigner
	}
	c := s.config
	return SignerHealth{true, "live", 196, c.ShopID, c.Checkout, c.Merchant, c.QuoteSigner}, nil
}
func (s *liveTestSigner) Sign(_ context.Context, q SignRequest) (SignedQuote, error) {
	n := s.signCalls.Add(1)
	now := uint64(time.Now().UnixMilli())
	fx := domain.FXSnapshot{Source: "okx", InstrumentID: "OKB-USDT", PriceField: "last", RateUSDTPerOKB: "117.91", SourceTimestampMS: now, FetchedAtMS: now, AmountUSDT: q.AmountUSDT, Rounding: "ceil_wei", PolicyVersion: "okx_spot_last_v1", IssuedAtMS: now}
	wei, _ := fx.QuoteAmountWei()
	v := SignedQuote{Domain: SigningDomain{"TapeCommerceCheckout", "1", 196, s.config.Checkout}, Types: map[string][]TypedField{"Invoice": invoiceFields}, Invoice: SignedInvoice{fmtHash(n), s.config.TenantHash, q.Payer, s.config.Merchant, wei, strconv.FormatUint(now/1000+60, 10)}, Signature: demoSignature, FXSnapshot: fx}
	if s.alter != nil {
		s.alter(&v)
	}
	return v, nil
}
func fmtHash(n int64) string {
	return "0x" + strings.Repeat("0", 64-len(strconv.FormatInt(n, 16))) + strconv.FormatInt(n, 16)
}
func liveFixture(t *testing.T) (*Demo, *Live, uint, *orderdomain.Order, *liveTestSigner) {
	d, buyer, i := newBridgeFixture(t)
	order, e := orderstore.New(d.DB, d.Bridge.GuestSecret).GetByID(i.OrderID)
	if e != nil {
		t.Fatal(e)
	}
	c := config.TapeCommerceConfig{Enabled: true, ShopID: d.Bridge.ShopID, Checkout: d.Bridge.Checkout, Merchant: d.Bridge.Merchant, TenantHash: d.Bridge.TenantHash, ChannelID: d.Bridge.ChannelID, QuoteSigner: ba("9")}
	signer := &liveTestSigner{config: c}
	l := &Live{Config: c, Bridge: d.Bridge, Orders: d.Orders, Signer: signer, statusDB: d.DB, done: make(chan struct{})}
	payments := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{ProductRepo: productstore.NewProductStore(d.DB)})
	d.Bridge.ChannelPolicy = func(tx *gorm.DB, o *orderdomain.Order, ch *paymentdomain.PaymentChannel) error {
		return payments.ValidateVerifiedStockChannel(o, ch, paymentstore.UseTransaction(tx, d.Bridge.GuestSecret).Products())
	}
	return d, l, buyer, order, signer
}
func liveRouter(l *Live, user uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(reseller.WithTenantContext(c.Request.Context(), reseller.MainTenantContext("store.test")))
		if user > 0 {
			c.Set("user_id", user)
		}
		c.Next()
	})
	RegisterPublicRoutes(r.Group("/public"), l)
	RegisterUserRoutes(r.Group(""), l)
	g := r.Group("/guest")
	RegisterGuestRoutes(g, g, l)
	return r
}
func liveRequest(t *testing.T, r http.Handler, method, path, body, auth string, status int) map[string]json.RawMessage {
	t.Helper()
	q := httptest.NewRequest(method, path, strings.NewReader(body))
	q.Header.Set("Content-Type", "application/json")
	if auth != "" {
		q.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	if w.Code != status {
		t.Fatalf("%s %s status %d: %s", method, path, w.Code, w.Body.String())
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal("invalid JSON")
	}
	return v
}
func TestLiveMemberHTTPOriginalOrderAndDurableWorker(t *testing.T) {
	d, l, buyer, o, s := liveFixture(t)
	r := liveRouter(l, buyer)
	path := "/orders/" + o.OrderNo + "/tapecommerce"
	liveRequest(t, r, "GET", path+"/status", "", "", 404)
	liveRequest(t, r, "POST", path+"/quote", `{"payer":"`+ba("4")+`","amountUSDT":"0.01"}`, "", 400)
	if s.signCalls.Load() != 0 {
		t.Fatal("public price reached signer")
	}
	got := liveRequest(t, r, "POST", path+"/quote", `{"payer":"`+ba("4")+`"}`, "", 200)
	var q PublicQuote
	if json.Unmarshal(got["data"], &q) != nil {
		t.Fatal("quote decode")
	}
	repeat := liveRequest(t, r, "POST", path+"/quote", `{"payer":"`+ba("4")+`"}`, "", 200)
	if !bytes.Equal(got["data"], repeat["data"]) || s.signCalls.Load() != 1 {
		t.Fatal("repeat quote signed another invoice")
	}
	liveRequest(t, r, "POST", path+"/quote", `{"payer":"`+ba("8")+`"}`, "", 409)
	liveRequest(t, liveRouter(l, buyer+99), "GET", path+"/status", "", "", 404)
	// Persist the hash, then stop all HTTP polling: the durable worker alone must
	// settle original order/payment/card rows using synthetic chain evidence.
	if e := d.Bridge.service().Submit(context.Background(), q.InvoiceID, bh("6")); e != nil {
		t.Fatal(e)
	}
	if e := l.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	state, e := l.Status(context.Background(), Owner{OrderID: o.ID, UserID: buyer})
	if e != nil || state.State != domain.Delivered {
		t.Fatal(state, e)
	}
	used, deliveries := counts(t, d)
	if used != 1 || deliveries != 1 {
		t.Fatal("original stock", used, deliveries)
	}
	liveRequest(t, r, "POST", path+"/submit", `{"invoice_id":"`+q.InvoiceID+`","transaction_hash":"`+bh("6")+`"}`, "", 200)
	used2, deliveries2 := counts(t, d)
	if used2 != used || deliveries2 != deliveries {
		t.Fatal("replay consumed stock")
	}
}
func guestOrder(t *testing.T, d *Demo, email, password string) *orderdomain.Order {
	t.Helper()
	if e := d.DB.Model(&productdomain.Product{}).Where("id = ?", d.ProductID).Update("purchase_type", constants.ProductPurchaseGuest).Error; e != nil {
		t.Fatal(e)
	}
	o, e := d.Orders.CreateGuestOrder(orderapp.CreateGuestOrderInput{Email: email, OrderPassword: password, Tenant: reseller.MainTenantContext("store.test"), Items: []orderapp.CreateOrderItem{{ProductID: d.ProductID, Quantity: 1, FulfillmentType: constants.FulfillmentTypeAuto}}, ClientIP: "127.0.0.1"})
	if e != nil {
		t.Fatal("original guest create:", e)
	}
	return o
}
func TestLiveGuestOriginalSplitOrderAuthorizationAndDelivery(t *testing.T) {
	d, l, _, _, _ := liveFixture(t)
	o := guestOrder(t, d, "customer@example.invalid", "test-guest-password")
	other := guestOrder(t, d, "customer@example.invalid", "test-guest-password")
	if len(o.Children) != 1 || o.UserID != 0 || o.Children[0].UserID != 0 || o.Children[0].GuestEmail == "" || o.Children[0].GuestPassword == "" {
		t.Fatal("original guest child credentials missing")
	}
	auth := "Guest " + base64.RawURLEncoding.EncodeToString([]byte("customer@example.invalid\ntest-guest-password"))
	bad := "Guest " + base64.RawURLEncoding.EncodeToString([]byte("customer@example.invalid\nwrong-password"))
	r := liveRouter(l, 0)
	path := "/guest/orders/" + o.OrderNo + "/tapecommerce"
	liveRequest(t, r, "POST", path+"/quote", `{"payer":"`+ba("4")+`"}`, bad, 404)
	got := liveRequest(t, r, "POST", path+"/quote", `{"payer":"`+ba("4")+`"}`, auth, 200)
	var q PublicQuote
	_ = json.Unmarshal(got["data"], &q)
	liveRequest(t, r, "POST", "/guest/orders/"+other.OrderNo+"/tapecommerce/submit", `{"invoice_id":"`+q.InvoiceID+`","transaction_hash":"`+bh("7")+`"}`, auth, 404)
	liveRequest(t, r, "POST", path+"/submit", `{"invoice_id":"`+q.InvoiceID+`","transaction_hash":"`+bh("7")+`"}`, auth, 200)
	state, e := l.Status(context.Background(), Owner{OrderID: o.ID, Guest: true})
	if e != nil || state.State != domain.Delivered {
		t.Fatal(state, e)
	}
	current, e := d.Orders.GetAnyOrderByGuestOrderNoForTenant(reseller.MainTenantContext("store.test"), o.OrderNo, "customer@example.invalid", "test-guest-password")
	if e != nil || current.Status != constants.OrderStatusCompleted || len(current.Children) != 1 || current.Children[0].Fulfillment == nil || current.Children[0].Fulfillment.Payload == "" {
		t.Fatal("original guest authorized fulfillment missing", e)
	}
	used, delivered := counts(t, d)
	if used != 1 || delivered != 1 {
		t.Fatal(used, delivered)
	}
}
func TestLiveTrustedSignerCannotChangeOrderAmountOrPayer(t *testing.T) {
	for _, field := range []string{"amount", "payer", "domain"} {
		t.Run(field, func(t *testing.T) {
			d, l, buyer, o, s := liveFixture(t)
			s.alter = func(q *SignedQuote) {
				switch field {
				case "amount":
					q.FXSnapshot.AmountUSDT = "0.01"
					q.Invoice.AmountWei, _ = q.FXSnapshot.QuoteAmountWei()
				case "payer":
					q.Invoice.Payer = ba("7")
				case "domain":
					q.Domain.ChainID = 1
				}
			}
			if _, e := l.Quote(context.Background(), Owner{OrderID: o.ID, UserID: buyer}, ba("4")); e == nil {
				t.Fatal("accepted changed " + field)
			}
			if _, e := tapestore.New(d.DB).ByOrder(context.Background(), d.Bridge.ShopID, o.ID); !errors.Is(e, domain.ErrNotFound) {
				t.Fatal("invalid quote persisted", e)
			}
		})
	}
}
func TestLiveReadinessCacheAndDisabledStatus(t *testing.T) {
	d, l, buyer, o, s := liveFixture(t)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !l.PublicConfig(context.Background()).Ready {
				t.Error("not ready")
			}
		}()
	}
	wg.Wait()
	if s.healthCalls.Load() != 1 {
		t.Fatal("anonymous health amplified", s.healthCalls.Load())
	}
	q, e := l.Quote(context.Background(), Owner{OrderID: o.ID, UserID: buyer}, ba("4"))
	if e != nil {
		t.Fatal(e)
	}
	if s.healthCalls.Load() != 1 || s.signCalls.Load() != 1 {
		t.Fatal("quote must call the fresh Sign endpoint, not return the cached public readiness")
	}
	disabled := NewLive(config.TapeCommerceConfig{ShopID: d.Bridge.ShopID}, d.DB, d.Bridge.GuestSecret, d.Orders, nil, nil)
	st, e := disabled.Status(context.Background(), Owner{OrderID: o.ID, UserID: buyer})
	if e != nil || st.InvoiceID != q.InvoiceID || st.Reason != "channel_disabled" {
		t.Fatal(st, e)
	}
	other := guestOrder(t, d, "disabled@example.invalid", "test-password")
	liveRequest(t, liveRouter(disabled, 0), "GET", "/guest/orders/"+other.OrderNo+"/tapecommerce/status", "", "Guest "+base64.RawURLEncoding.EncodeToString([]byte("disabled@example.invalid\ntest-password")), 404)
}
func TestLiveDisabledNoMigrationAndIdempotentLifecycle(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "disabled.db")), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	sql, _ := db.DB()
	defer sql.Close()
	l := NewLive(config.TapeCommerceConfig{Migrate: true}, db, "", nil, nil, nil)
	if l.PublicConfig(context.Background()).Ready || db.Migrator().HasTable("tapecommerce_invoices") {
		t.Fatal("disabled migrated/ready")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for n := 0; n < 3; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := l.Start(ctx); e != nil {
				t.Error(e)
			}
		}()
	}
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if e := l.Stop(stopCtx); e != nil {
		t.Fatal(e)
	}
	if e := l.Stop(stopCtx); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
}
func TestHTTPSignerSecretAndRedirectProtection(t *testing.T) {
	var leaked atomic.Bool
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer dest.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 302) }))
	defer source.Close()
	secret := filepath.Join(t.TempDir(), "secret")
	if e := os.WriteFile(secret, []byte(strings.Repeat("s", 32)), 0600); e != nil {
		t.Fatal(e)
	}
	client, e := NewHTTPSigner(config.TapeCommerceConfig{SignerURL: source.URL, SignerSecretFile: secret})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = client.Health(context.Background()); e == nil || leaked.Load() {
		t.Fatal("followed signer redirect")
	}
	if e = os.Chmod(secret, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = NewHTTPSigner(config.TapeCommerceConfig{SignerURL: source.URL, SignerSecretFile: secret}); e == nil {
		t.Fatal("accepted shared secret file")
	}
}

func TestLiveExistingQuoteRejectsCanceledPaidAndExpiredOrder(t *testing.T) {
	for _, state := range []string{"canceled", "paid", "expired"} {
		t.Run(state, func(t *testing.T) {
			d, l, buyer, o, s := liveFixture(t)
			owner := Owner{OrderID: o.ID, UserID: buyer}
			q, e := l.Quote(context.Background(), owner, ba("4"))
			if e != nil {
				t.Fatal(e)
			}
			switch state {
			case "canceled":
				if _, e = d.Orders.CancelOrder(o.ID, buyer); e != nil {
					t.Fatal(e)
				}
			case "paid":
				if e = d.DB.Model(&orderdomain.Order{}).Where("id = ?", o.ID).Updates(map[string]any{"status": constants.OrderStatusPaid, "paid_at": time.Now()}).Error; e != nil {
					t.Fatal(e)
				}
			case "expired":
				oldMS := uint64(time.Now().Add(-2 * time.Minute).UnixMilli())
				if e = d.DB.Table("tapecommerce_invoices").Where("invoice_id = ?", q.InvoiceID).Updates(map[string]any{"deadline": oldMS/1000 + 60, "fx_issued_at_ms": oldMS, "fx_source_timestamp_ms": oldMS, "fx_fetched_at_ms": oldMS}).Error; e != nil {
					t.Fatal(e)
				}
			}
			if _, e = l.Quote(context.Background(), owner, ba("4")); e == nil {
				t.Fatal("returned unsafe stored quote")
			}
			if state == "expired" && !errors.Is(e, ErrExpired) {
				t.Fatal(e)
			}
			if s.signCalls.Load() != 1 {
				t.Fatal("replaced historical signature")
			}
		})
	}
}
func TestLiveConfiguredChannelPolicyRejectsBeforePersist(t *testing.T) {
	d, l, buyer, o, _ := liveFixture(t)
	if e := d.DB.Model(&paymentdomain.PaymentChannel{}).Where("id = ?", d.Bridge.ChannelID).Update("max_amount", "0.50").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := l.Quote(context.Background(), Owner{OrderID: o.ID, UserID: buyer}, ba("4")); e == nil {
		t.Fatal("bypassed channel amount policy")
	}
	if _, e := tapestore.New(d.DB).ByOrder(context.Background(), d.Bridge.ShopID, o.ID); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("policy rejected invoice persisted", e)
	}
}

func TestLiveChannelMetadataRegistrationIsStrict(t *testing.T) {
	svc := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{})
	channel := &paymentdomain.PaymentChannel{ProviderType: "tapecommerce", ChannelType: "okb", InteractionMode: "wallet"}
	if e := svc.ValidateChannel(channel); e != nil {
		t.Fatal(e)
	}
	channel.ChannelType = "usdt"
	if e := svc.ValidateChannel(channel); e == nil {
		t.Fatal("accepted wrong native channel")
	}
	channel.ChannelType = "okb"
	channel.InteractionMode = "redirect"
	if e := svc.ValidateChannel(channel); e == nil {
		t.Fatal("accepted redirect gateway mode")
	}
}

func TestLiveProviderCannotCreateGenericPaymentOrReserveWallet(t *testing.T) {
	d, _, _, o, _ := liveFixture(t)
	svc := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{OrderStore: orderstore.New(d.DB, d.Bridge.GuestSecret), PaymentStore: paymentstore.New(d.DB, d.Bridge.GuestSecret), ChannelStore: paymentstore.NewChannelStore(d.DB)})
	for _, balance := range []bool{false, true} {
		if _, e := svc.CreatePayment(paymentapp.CreatePaymentInput{OrderID: o.ID, ChannelID: d.Bridge.ChannelID, UseBalance: balance, Context: context.Background()}); !errors.Is(e, paymentapp.ErrPaymentProviderNotSupported) {
			t.Fatal("generic provider result", e)
		}
	}
	var count int64
	if e := d.DB.Model(&paymentdomain.Payment{}).Where("order_id = ?", o.ID).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("generic payment wrote rows", count, e)
	}
	current, e := orderstore.New(d.DB, d.Bridge.GuestSecret).GetByID(o.ID)
	if e != nil || current.Status != constants.OrderStatusPendingPayment || !current.WalletPaidAmount.IsZero() {
		t.Fatal("generic payment changed order", e)
	}
}
