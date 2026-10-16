package application_test

import (
	"errors"
	"github.com/dujiao-next/internal/factorysupply"
	. "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	fulfillmentgormstore "github.com/dujiao-next/internal/modules/fulfillment/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	ordergormstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"testing"
)

func TestMainSupplyOrderCannotUseMerchantManualDeliveryAfterRename(t *testing.T) {
	db := setupFulfillmentServiceTestDB(t)
	order := orderdomain.Order{OrderNo: "main-supply-no-fallback", Status: "paid", Currency: "USDT"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := orderdomain.OrderItem{OrderID: order.ID, ProductID: 1, SKUID: 1, Quantity: 1, TitleJSON: jsonmap.JSON{"zh-CN": "已改名商品"}, SupplyBrand: "ChatGPT", SupplySource: "main", FulfillmentType: "upstream"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	svc := New(Options{OrderStore: ordergormstore.New(db, "test-guest-credential-secret-with-32-bytes"), FulfillmentStore: fulfillmentgormstore.New(db)})
	if _, err := svc.CreateManual(CreateManualInput{OrderID: order.ID, AdminID: 1, Payload: "merchant substitute"}); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	var count int64
	db.Model(&fulfillmentdomain.Fulfillment{}).Count(&count)
	if count != 0 {
		t.Fatal("merchant fulfilled main supply")
	}
	if err := db.Model(&item).Update("supply_source", "merchant").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateManual(CreateManualInput{OrderID: order.ID, AdminID: 1, Payload: "other brand own delivery"}); err != nil {
		t.Fatal(err)
	}
}
