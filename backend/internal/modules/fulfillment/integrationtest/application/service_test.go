package application_test

import (
	"errors"
	"fmt"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	fulfillmentgormstore "github.com/dujiao-next/internal/modules/fulfillment/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"

	"github.com/dujiao-next/internal/constants"
	cardsecretdomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	"github.com/dujiao-next/internal/platform/database/gormdb"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupFulfillmentServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:fulfillment_service_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&orderdomain.Order{},
		&orderdomain.OrderItem{},
		&fulfillmentdomain.Fulfillment{},
		&cardsecretdomain.Secret{},
		&cardsecretdomain.Batch{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	gormdb.DB = db
	return db
}

func TestCreateAutoFulfillmentRespectsSKUBoundary(t *testing.T) {
	db := setupFulfillmentServiceTestDB(t)
	now := time.Now()

	order := &orderdomain.Order{
		OrderNo:                 "FULFILL-SKU-001",
		UserID:                  1,
		Status:                  constants.OrderStatusPaid,
		Currency:                "CNY",
		OriginalAmount:          money.FromDecimal(decimal.NewFromInt(10)),
		DiscountAmount:          money.FromDecimal(decimal.Zero),
		PromotionDiscountAmount: money.FromDecimal(decimal.Zero),
		TotalAmount:             money.FromDecimal(decimal.NewFromInt(10)),
		WalletPaidAmount:        money.FromDecimal(decimal.Zero),
		OnlinePaidAmount:        money.FromDecimal(decimal.NewFromInt(10)),
		RefundedAmount:          money.FromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	orderItem := &orderdomain.OrderItem{
		OrderID:         order.ID,
		ProductID:       100,
		SKUID:           1001,
		TitleJSON:       jsonmap.JSON{"zh-CN": "测试商品"},
		UnitPrice:       money.FromDecimal(decimal.NewFromInt(10)),
		Quantity:        1,
		TotalPrice:      money.FromDecimal(decimal.NewFromInt(10)),
		FulfillmentType: constants.FulfillmentTypeAuto,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(orderItem).Error; err != nil {
		t.Fatalf("create order item failed: %v", err)
	}

	secretTarget := &cardsecretdomain.Secret{
		ProductID: 100,
		SKUID:     1001,
		Secret:    "SECRET-SKU-1001",
		Status:    cardsecretdomain.StatusAvailable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(secretTarget).Error; err != nil {
		t.Fatalf("create target secret failed: %v", err)
	}
	secretOther := &cardsecretdomain.Secret{
		ProductID: 100,
		SKUID:     1002,
		Secret:    "SECRET-SKU-1002",
		Status:    cardsecretdomain.StatusAvailable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(secretOther).Error; err != nil {
		t.Fatalf("create other secret failed: %v", err)
	}

	svc := New(Options{
		OrderStore:       ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes"),
		FulfillmentStore: fulfillmentgormstore.New(db),
	})

	result, err := svc.CreateAuto(order.ID)
	if err != nil {
		t.Fatalf("create auto fulfillment failed: %v", err)
	}
	if result == nil {
		t.Fatalf("fulfillment should not be nil")
	}
	if !strings.Contains(result.Payload, "SECRET-SKU-1001") {
		t.Fatalf("payload should contain target sku secret, got: %s", result.Payload)
	}
	if strings.Contains(result.Payload, "SECRET-SKU-1002") {
		t.Fatalf("payload should not contain other sku secret, got: %s", result.Payload)
	}

	var targetAfter cardsecretdomain.Secret
	if err := db.First(&targetAfter, secretTarget.ID).Error; err != nil {
		t.Fatalf("query target secret failed: %v", err)
	}
	if targetAfter.Status != cardsecretdomain.StatusUsed {
		t.Fatalf("target secret status want used got %s", targetAfter.Status)
	}

	var otherAfter cardsecretdomain.Secret
	if err := db.First(&otherAfter, secretOther.ID).Error; err != nil {
		t.Fatalf("query other secret failed: %v", err)
	}
	if otherAfter.Status != cardsecretdomain.StatusAvailable {
		t.Fatalf("other secret status should stay available got %s", otherAfter.Status)
	}

	var orderAfter orderdomain.Order
	if err := db.First(&orderAfter, order.ID).Error; err != nil {
		t.Fatalf("query order failed: %v", err)
	}
	if orderAfter.Status != constants.OrderStatusCompleted {
		t.Fatalf("order status want completed got %s", orderAfter.Status)
	}
}

// Concurrent refund wins after the optimistic read but before the fulfillment
// transaction. CreateAuto must observe the locked row instead of issuing cards.
type nativeStatusRaceStore struct {
	ordercontract.Store
	db *gorm.DB
}

func (s nativeStatusRaceStore) WithinTransaction(fn func(ordercontract.Transaction) error) error {
	if err := s.db.Model(&orderdomain.Order{}).Where("order_no = ?", "NATIVE-REFUND-RACE").Update("status", constants.OrderStatusRefunded).Error; err != nil {
		return err
	}
	return s.Store.WithinTransaction(fn)
}
func TestNativeAutoDoesNotOverwriteConcurrentRefund(t *testing.T) {
	db := setupFulfillmentServiceTestDB(t)
	sql, _ := db.DB()
	defer sql.Close()
	o := orderdomain.Order{OrderNo: "NATIVE-REFUND-RACE", Status: constants.OrderStatusPaid, Currency: "USDT"}
	if e := db.Create(&o).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&orderdomain.OrderItem{OrderID: o.ID, ProductID: 77, SKUID: 78, TitleJSON: jsonmap.JSON{"zh-CN": "native"}, Quantity: 1, FulfillmentType: "auto"}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&cardsecretdomain.Secret{ProductID: 77, SKUID: 78, Secret: "TEST-NOT-DELIVERED", Status: "available"}).Error; e != nil {
		t.Fatal(e)
	}
	store := nativeStatusRaceStore{Store: ordergormstore.New(db, "isolated-test-secret"), db: db}
	svc := New(Options{OrderStore: store, FulfillmentStore: fulfillmentgormstore.New(db)})
	if _, e := svc.CreateAuto(o.ID); !errors.Is(e, ErrOrderStatusInvalid) {
		t.Fatalf("stale paid snapshot accepted: %v", e)
	}
	var count int64
	db.Model(&fulfillmentdomain.Fulfillment{}).Count(&count)
	if count != 0 {
		t.Fatal("refund issued a card")
	}
	db.First(&o, o.ID)
	if o.Status != constants.OrderStatusRefunded {
		t.Fatal("refund overwritten")
	}
	db.Model(&cardsecretdomain.Secret{}).Where("status = ?", "available").Count(&count)
	if count != 1 {
		t.Fatal("stock consumed on refund")
	}
}

func TestNativeAutoConcurrentRetriesConsumeExactlyN(t *testing.T) {
	db := setupFulfillmentServiceTestDB(t)
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	o := orderdomain.Order{OrderNo: "NATIVE-CONCURRENT", Status: constants.OrderStatusPaid, Currency: "USDT"}
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	item := orderdomain.OrderItem{OrderID: o.ID, ProductID: 77, SKUID: 78, TitleJSON: jsonmap.JSON{"zh-CN": "native"}, Quantity: 2, FulfillmentType: "auto"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := db.Create(&cardsecretdomain.Secret{ProductID: 77, SKUID: 78, Secret: fmt.Sprintf("TEST-CARD-%d", i), Status: "available"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := New(Options{OrderStore: ordergormstore.New(db, "isolated-test-secret"), FulfillmentStore: fulfillmentgormstore.New(db)})
	var wg sync.WaitGroup
	var success atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreateAuto(o.ID)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrOrderStatusInvalid) && !errors.Is(err, ErrFulfillmentExists) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("deliveries=%d", success.Load())
	}
	var used, available, records int64
	db.Model(&cardsecretdomain.Secret{}).Where("status = ?", "used").Count(&used)
	db.Model(&cardsecretdomain.Secret{}).Where("status = ?", "available").Count(&available)
	db.Model(&fulfillmentdomain.Fulfillment{}).Count(&records)
	if used != 2 || available != 1 || records != 1 {
		t.Fatalf("used=%d available=%d records=%d", used, available, records)
	}
}

func TestNativeAutoParentStatusCommitsWithCardsWithoutNotificationQueue(t *testing.T) {
	for _, failParent := range []bool{false, true} {
		t.Run(fmt.Sprint(failParent), func(t *testing.T) {
			db := setupFulfillmentServiceTestDB(t)
			pool, _ := db.DB()
			defer pool.Close()
			parent := orderdomain.Order{OrderNo: "NATIVE-PARENT", Status: constants.OrderStatusPaid, Currency: "USDT"}
			if e := db.Create(&parent).Error; e != nil {
				t.Fatal(e)
			}
			child := orderdomain.Order{OrderNo: "NATIVE-CHILD", ParentID: &parent.ID, Status: constants.OrderStatusPaid, Currency: "USDT"}
			if e := db.Create(&child).Error; e != nil {
				t.Fatal(e)
			}
			if e := db.Create(&orderdomain.OrderItem{OrderID: child.ID, ProductID: 77, SKUID: 78, TitleJSON: jsonmap.JSON{"zh-CN": "native"}, Quantity: 1, FulfillmentType: "auto"}).Error; e != nil {
				t.Fatal(e)
			}
			if e := db.Create(&cardsecretdomain.Secret{ProductID: 77, SKUID: 78, Secret: "TEST-PARENT-ATOMIC", Status: "available"}).Error; e != nil {
				t.Fatal(e)
			}
			if failParent {
				if e := db.Exec(fmt.Sprintf("CREATE TRIGGER fail_parent BEFORE UPDATE OF status ON orders WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'isolated parent failure'); END", parent.ID)).Error; e != nil {
					t.Fatal(e)
				}
			}
			svc := New(Options{OrderStore: ordergormstore.New(db, "isolated-test-secret"), FulfillmentStore: fulfillmentgormstore.New(db)})
			_, err := svc.CreateAuto(child.ID)
			var used, records int64
			db.Model(&cardsecretdomain.Secret{}).Where("status = ?", "used").Count(&used)
			db.Model(&fulfillmentdomain.Fulfillment{}).Count(&records)
			db.First(&parent, parent.ID)
			db.First(&child, child.ID)
			if failParent {
				if err == nil || used != 0 || records != 0 || parent.Status != "paid" || child.Status != "paid" {
					t.Fatalf("non-atomic parent failure: %v used=%d records=%d parent=%s child=%s", err, used, records, parent.Status, child.Status)
				}
			} else if err != nil || used != 1 || records != 1 || parent.Status != "completed" || child.Status != "completed" {
				t.Fatalf("parent not committed: %v %+v", err, parent)
			}
		})
	}
}
