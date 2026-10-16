package selfhost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	card "github.com/dujiao-next/internal/modules/cardsecret/domain"
	product "github.com/dujiao-next/internal/modules/catalog/product/domain"
	products "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	coupon "github.com/dujiao-next/internal/modules/coupon/domain"
	coupons "github.com/dujiao-next/internal/modules/coupon/infrastructure/gormstore"
	downstream "github.com/dujiao-next/internal/modules/downstreamcallback/domain"
	downstreamstore "github.com/dujiao-next/internal/modules/downstreamcallback/infrastructure/gormstore"
	fulfill "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillment "github.com/dujiao-next/internal/modules/fulfillment/domain"
	fulfillstore "github.com/dujiao-next/internal/modules/fulfillment/infrastructure/gormstore"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	order "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	orderpresenter "github.com/dujiao-next/internal/modules/order/transport/presenter"
	payment "github.com/dujiao-next/internal/modules/payment/application"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	promotion "github.com/dujiao-next/internal/modules/promotion/domain"
	promotions "github.com/dujiao-next/internal/modules/promotion/infrastructure/gormstore"
	settings "github.com/dujiao-next/internal/modules/settings/application"
	settingstore "github.com/dujiao-next/internal/modules/settings/infrastructure/gormstore"
	upstream "github.com/dujiao-next/internal/modules/upstreamapi/transport/http"
	wallet "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type nativeQueue struct {
	service *fulfill.Service
	errors  []error
	jobs    int
}

func (*nativeQueue) Enabled() bool                                                { return true }
func (*nativeQueue) EnqueueTimeoutCancel(uint, time.Duration) error               { return nil }
func (*nativeQueue) EnqueueStatusEmail(uint, string) error                        { return nil }
func (*nativeQueue) EnqueueBotNotification(paymentcontract.BotNotification) error { return nil }
func (*nativeQueue) EnqueueWalletRechargeExpire(uint, time.Duration) error        { return nil }
func (q *nativeQueue) EnqueueOrderAutoFulfill(id uint) error {
	q.jobs++
	_, err := q.service.CreateAuto(id)
	if err != nil {
		q.errors = append(q.errors, err)
	}
	return err
}

type nativeOrderAdapter struct{ service *orderapp.OrderService }

func (a nativeOrderAdapter) CreateOrder(in upstream.CreateOrderInput) (*order.Order, error) {
	items := make([]orderapp.CreateOrderItem, len(in.Items))
	for i, v := range in.Items {
		items[i] = orderapp.CreateOrderItem{ProductID: v.ProductID, SKUID: v.SKUID, Quantity: v.Quantity, FulfillmentType: v.FulfillmentType}
	}
	return a.service.CreateOrder(orderapp.CreateOrderInput{UserID: in.UserID, ClientRequestID: in.ClientRequestID, RequestMetadata: in.RequestMetadata, Items: items, SkipRiskControl: true})
}
func (a nativeOrderAdapter) GetOrderByUser(id, uid uint) (*order.Order, error) {
	return a.service.GetOrderByUser(id, uid)
}
func (a nativeOrderAdapter) CancelOrder(id, uid uint) (*order.Order, error) {
	return a.service.CancelOrder(id, uid)
}
func (a nativeOrderAdapter) BuildLocalRefundRecordsForOrder(o *order.Order) ([]jsonmap.JSON, error) {
	return a.service.BuildLocalRefundRecordsForOrder(o)
}

type nativePaymentAdapter struct{ service *payment.PaymentService }

func (a nativePaymentAdapter) CreatePayment(in upstream.CreatePaymentInput) (*upstream.CreatePaymentResult, error) {
	r, e := a.service.CreatePayment(payment.CreatePaymentInput{OrderID: in.OrderID, UseBalance: in.UseBalance})
	if e != nil {
		return nil, e
	}
	return &upstream.CreatePaymentResult{OrderPaid: r.OrderPaid}, nil
}

func TestNativeStockGuestChainMemberWalletAndAPI(t *testing.T) {
	for _, flow := range []string{"guest_chain", "member_wallet", "open_api"} {
		t.Run(flow, func(t *testing.T) {
			q := &nativeQueue{}
			f := setupWithQueue(t, q)
			// Keep the voucher bridge enabled but make every accidental external call fail.
			t.Setenv("VOUCHER_SERVICE_URL", "http://127.0.0.1:1")
			t.Setenv("VOUCHER_DELIVERY_KEY", "isolated-native-card-test-key")
			if e := f.db.AutoMigrate(&card.Batch{}, &card.Secret{}, &coupon.Coupon{}, &coupon.CouponUsage{}, &promotion.Promotion{}, &settingstore.SettingRecord{}, &downstream.OrderRef{}, &order.OrderRefundRecord{}); e != nil {
				t.Fatal(e)
			}
			ss := settingstore.New(f.db)
			_, e := ss.Upsert(constants.SettingKeySiteConfig, jsonmap.JSON{constants.SettingFieldSiteCurrency: "USDT"})
			if e != nil {
				t.Fatal(e)
			}
			setting := settings.NewService(ss)
			os := orderstore.New(f.db, "synthetic-guest-secret-only")
			orders := orderapp.NewOrderService(orderapp.OrderServiceOptions{OrderStore: os, ProductStore: products.NewProductStore(f.db), ProductSKUStore: products.NewSKUStore(f.db), CouponStore: coupons.New(f.db), CouponUsageStore: coupons.NewUsageStore(f.db), PromotionRepo: promotions.New(f.db), SettingService: setting, Queue: q})
			q.service = fulfill.New(fulfill.Options{OrderStore: os, FulfillmentStore: fulfillstore.New(f.db), OrderQueue: q})
			p := product.Product{Slug: "native", TitleJSON: jsonmap.JSON{"zh-CN": "隔离库存商品"}, FulfillmentType: "auto", PurchaseType: "guest", IsActive: true, PriceAmount: money.FromDecimal(decimal.NewFromInt(10))}
			if e = f.db.Create(&p).Error; e != nil {
				t.Fatal(e)
			}
			sku := product.ProductSKU{ProductID: p.ID, SKUCode: "native", IsActive: true, PriceAmount: p.PriceAmount}
			if e = f.db.Create(&sku).Error; e != nil {
				t.Fatal(e)
			}
			for i := range 3 {
				if e = f.db.Create(&card.Secret{ProductID: p.ID, SKUID: sku.ID, Secret: fmt.Sprintf("TEST-ACCOUNT-%d\nTEST-PASSWORD-%d", i, i), Status: "available"}).Error; e != nil {
					t.Fatal(e)
				}
			}
			items := []orderapp.CreateOrderItem{{ProductID: p.ID, SKUID: sku.ID, Quantity: 2, FulfillmentType: "auto"}}
			if flow != "guest_chain" {
				if e = f.db.Create(&wallet.Account{UserID: f.user.ID, Balance: money.FromDecimal(decimal.NewFromInt(100))}).Error; e != nil {
					t.Fatal(e)
				}
			}
			var created *order.Order
			if flow == "guest_chain" {
				created, e = orders.CreateGuestOrder(orderapp.CreateGuestOrderInput{Email: "isolated@example.test", OrderPassword: "isolated-password", ClientRequestID: "native-guest", ExpectedCurrency: "USDT", ExpectedTotal: "20", Items: items})
			} else if flow == "member_wallet" {
				created, e = orders.CreateOrder(orderapp.CreateOrderInput{UserID: f.user.ID, Items: items})
			}
			if e != nil {
				t.Fatal(e)
			}
			if flow == "open_api" {
				h := &upstream.Handler{Dependencies: upstream.Dependencies{SKUs: products.NewSKUStore(f.db), ProductRepository: products.NewProductStore(f.db), Orders: nativeOrderAdapter{orders}, Payments: nativePaymentAdapter{f.service}, Settings: setting, DownstreamRefs: downstreamstore.New(f.db)}}
				router := gin.New()
				router.Use(func(c *gin.Context) { c.Set("upstream_user_id", f.user.ID); c.Set("upstream_credential_id", uint(1)) })
				router.POST("/orders", h.CreateOrder)
				router.GET("/orders/:id", h.GetOrder)
				body := fmt.Sprintf(`{"sku_id":%d,"quantity":2,"client_order_id":"native-api-1","max_total":"20"}`, sku.ID)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString(body)))
				var response struct {
					OK      bool `json:"ok"`
					OrderID uint `json:"order_id"`
				}
				if e = json.Unmarshal(rec.Body.Bytes(), &response); e != nil || !response.OK {
					t.Fatalf("API order failed: %s %v", rec.Body, e)
				}
				created, e = os.GetByID(response.OrderID)
				if e != nil {
					t.Fatal(e)
				}
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/orders/%d", created.ID), nil))
				if !strings.Contains(rec.Body.String(), "TEST-ACCOUNT-0") || !strings.Contains(rec.Body.String(), `"type":"auto"`) {
					t.Fatalf("API missing cards: %s", rec.Body)
				}
			} else {
				if created.Fulfillment != nil {
					t.Fatal("unpaid card leak")
				}
				for _, child := range created.Children {
					if child.Fulfillment != nil {
						t.Fatal("unpaid child card leak")
					}
				}
				if flow == "guest_chain" {
					pay, e := f.service.CreatePayment(payment.CreatePaymentInput{OrderID: created.ID, ChannelID: f.channel.ID, Context: context.Background()})
					if e != nil {
						t.Fatal(e)
					}
					if _, e = submit(f, pay.Payment, "0x"+strings.Repeat("a", 64)); e != nil {
						t.Fatal(e)
					}
				} else {
					if _, e = f.service.CreatePayment(payment.CreatePaymentInput{OrderID: created.ID, UseBalance: true}); e != nil {
						t.Fatal(e)
					}
				}
			}
			if len(q.errors) != 0 || q.jobs != 1 {
				t.Fatalf("auto queue: jobs=%d errors=%v", q.jobs, q.errors)
			}
			final, e := os.GetByID(created.ID)
			if e != nil {
				t.Fatal(e)
			}
			if final.Status != "completed" || len(final.Children) != 1 || final.Children[0].Fulfillment == nil {
				t.Fatalf("not delivered: %+v", final)
			}
			got := final.Children[0].Fulfillment.Payload
			if got != "TEST-ACCOUNT-0\nTEST-PASSWORD-0\n\nTEST-ACCOUNT-1\nTEST-PASSWORD-1" {
				t.Fatalf("wrong quantity/boundary: %q", got)
			}
			var used, available, records int64
			f.db.Model(&card.Secret{}).Where("status = ?", "used").Count(&used)
			f.db.Model(&card.Secret{}).Where("status = ?", "available").Count(&available)
			f.db.Model(&fulfillment.Fulfillment{}).Count(&records)
			if used != 2 || available != 1 || records != 1 {
				t.Fatalf("stock used=%d available=%d fulfillment=%d", used, available, records)
			}
			if _, e = q.service.CreateAuto(final.Children[0].ID); e == nil {
				t.Fatal("duplicate delivery accepted")
			}
			// An unpaid order reserves only remaining stock; cancel releases it,
			// and a larger order rolls back instead of recycling the sold records.
			pending, err := orders.CreateOrder(orderapp.CreateOrderInput{UserID: f.user.ID, Items: []orderapp.CreateOrderItem{{ProductID: p.ID, SKUID: sku.ID, Quantity: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = orders.CancelOrder(pending.ID, f.user.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = orders.CreateOrder(orderapp.CreateOrderInput{UserID: f.user.ID, Items: items}); err == nil {
				t.Fatal("insufficient stock accepted")
			}
			f.db.Model(&card.Secret{}).Where("status = ?", "used").Count(&used)
			f.db.Model(&card.Secret{}).Where("status = ?", "available").Count(&available)
			if used != 2 || available != 1 {
				t.Fatalf("cancel recycled sold cards: used=%d available=%d", used, available)
			}
			dto := orderpresenter.NewOrderDetail(final)
			if dto.Children[0].Items[0].DeliveryMode != "stock" {
				t.Fatal("missing stock delivery mode")
			}
		})
	}
}
