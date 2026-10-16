package tapecommercebootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dujiao-next/internal/constants"
	carddomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	fulfillmentapp "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	fulfillmentstore "github.com/dujiao-next/internal/modules/fulfillment/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupStock(t *testing.T, status string, stock int) (StockFulfiller, domain.Job, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "stock.db")), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sql.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&orderdomain.Order{}, &orderdomain.OrderItem{}, &fulfillmentdomain.Fulfillment{}, &carddomain.Secret{}, &carddomain.Batch{}); err != nil {
		t.Fatal(err)
	}
	order := orderdomain.Order{OrderNo: "TAPECOMMERCE-ISOLATED", Status: status, Currency: "USDT"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := orderdomain.OrderItem{OrderID: order.ID, ProductID: 10, SKUID: 11, TitleJSON: jsonmap.JSON{"zh-CN": "合成授权测试商品"}, Quantity: 1, FulfillmentType: constants.FulfillmentTypeAuto}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	for n := 0; n < stock; n++ {
		card := carddomain.Secret{ProductID: 10, SKUID: 11, Secret: "SYNTHETIC-NON-REDEEMABLE-TEST", Status: carddomain.StatusAvailable}
		if err := db.Create(&card).Error; err != nil {
			t.Fatal(err)
		}
	}
	os := orderstore.New(db, "isolated-test-guest-credential-secret")
	fs := fulfillmentstore.New(db)
	auto := fulfillmentapp.New(fulfillmentapp.Options{OrderStore: os, FulfillmentStore: fs})
	id := "0x" + strings.Repeat("1", 64)
	job := domain.Job{InvoiceID: id, ShopID: "shop-1", OrderID: order.ID, Key: "tapecommerce:" + id}
	return StockFulfiller{ShopID: "shop-1", Orders: os, Deliveries: fs, Auto: auto}, job, db
}

// Uses the real existing CreateAuto service and its transaction/stock stores,
// not a fake fulfillment implementation. RPC/payment admission is not covered.
func TestRealStockAdapterConcurrentReplayConsumesOneCard(t *testing.T) {
	f, job, db := setupStock(t, constants.OrderStatusPaid, 2)
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.Fulfill(context.Background(), job); err != nil {
				t.Errorf("fulfill: %v", err)
			}
		}()
	}
	wg.Wait()
	var used, deliveries int64
	if err := db.Model(&carddomain.Secret{}).Where("status = ?", carddomain.StatusUsed).Count(&used).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&fulfillmentdomain.Fulfillment{}).Count(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if used != 1 || deliveries != 1 {
		t.Fatalf("used=%d deliveries=%d", used, deliveries)
	}
	var order orderdomain.Order
	if err := db.First(&order, job.OrderID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != constants.OrderStatusCompleted {
		t.Fatal(order.Status)
	}
}

func TestRealStockAdapterRejectsWrongShopUnpaidAndStockout(t *testing.T) {
	t.Run("wrong_shop", func(t *testing.T) {
		f, j, db := setupStock(t, constants.OrderStatusPaid, 1)
		j.ShopID = "other-shop"
		if err := f.Fulfill(context.Background(), j); !errors.Is(err, domain.ErrReview) {
			t.Fatal(err)
		}
		assertNoDelivery(t, db)
	})
	t.Run("unpaid", func(t *testing.T) {
		f, j, db := setupStock(t, constants.OrderStatusPendingPayment, 1)
		if err := f.Fulfill(context.Background(), j); !errors.Is(err, domain.ErrReview) {
			t.Fatal(err)
		}
		assertNoDelivery(t, db)
	})
	t.Run("stockout", func(t *testing.T) {
		f, j, db := setupStock(t, constants.OrderStatusPaid, 0)
		if err := f.Fulfill(context.Background(), j); !errors.Is(err, domain.ErrReview) {
			t.Fatal(err)
		}
		assertNoDelivery(t, db)
	})
}
func assertNoDelivery(t *testing.T, db *gorm.DB) {
	t.Helper()
	var count int64
	if err := db.Model(&fulfillmentdomain.Fulfillment{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("unexpected delivery")
	}
}
