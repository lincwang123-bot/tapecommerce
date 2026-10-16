package gormstore

import (
	"fmt"
	"testing"

	cardsecretdomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/shopspring/decimal"
)

func TestGetStockStatsUsesActiveManualSKUs(t *testing.T) {
	repo, db := setupDashboardRepositoryTest(t)
	category := createDashboardCategory(t, db, "dashboard-manual-stock")

	lowStockProduct := &productdomain.Product{
		CategoryID:       category.ID,
		Slug:             "manual-low-stock",
		TitleJSON:        jsonmap.JSON{"zh-CN": "多 SKU 手动商品"},
		PriceAmount:      money.FromDecimal(decimal.NewFromInt(99)),
		PurchaseType:     constants.ProductPurchaseMember,
		FulfillmentType:  constants.FulfillmentTypeManual,
		ManualStockTotal: 999,
		IsActive:         true,
	}
	if err := db.Create(lowStockProduct).Error; err != nil {
		t.Fatalf("create low stock product failed: %v", err)
	}
	for idx, sku := range []productdomain.ProductSKU{
		{ProductID: lowStockProduct.ID, SKUCode: "A", PriceAmount: money.FromDecimal(decimal.NewFromInt(99)), ManualStockTotal: 2, IsActive: true},
		{ProductID: lowStockProduct.ID, SKUCode: "B", PriceAmount: money.FromDecimal(decimal.NewFromInt(99)), ManualStockTotal: 3, IsActive: true},
		{ProductID: lowStockProduct.ID, SKUCode: "DISABLED", PriceAmount: money.FromDecimal(decimal.NewFromInt(99)), ManualStockTotal: 100, IsActive: false},
	} {
		row := sku
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create manual sku failed: %v", err)
		}
		if idx == 2 {
			if err := db.Model(&row).Update("is_active", false).Error; err != nil {
				t.Fatalf("disable manual sku failed: %v", err)
			}
		}
	}

	unlimitedProduct := &productdomain.Product{
		CategoryID:       category.ID,
		Slug:             "manual-unlimited-sku",
		TitleJSON:        jsonmap.JSON{"zh-CN": "无限库存商品"},
		PriceAmount:      money.FromDecimal(decimal.NewFromInt(88)),
		PurchaseType:     constants.ProductPurchaseMember,
		FulfillmentType:  constants.FulfillmentTypeManual,
		ManualStockTotal: 0,
		IsActive:         true,
	}
	if err := db.Create(unlimitedProduct).Error; err != nil {
		t.Fatalf("create unlimited product failed: %v", err)
	}
	unlimitedSKU := &productdomain.ProductSKU{
		ProductID:        unlimitedProduct.ID,
		SKUCode:          "UNLIMITED",
		PriceAmount:      money.FromDecimal(decimal.NewFromInt(88)),
		ManualStockTotal: constants.ManualStockUnlimited,
		IsActive:         true,
	}
	if err := db.Create(unlimitedSKU).Error; err != nil {
		t.Fatalf("create unlimited sku failed: %v", err)
	}

	outOfStockProduct := &productdomain.Product{
		CategoryID:       category.ID,
		Slug:             "manual-fallback-zero",
		TitleJSON:        jsonmap.JSON{"zh-CN": "回退零库存商品"},
		PriceAmount:      money.FromDecimal(decimal.NewFromInt(77)),
		PurchaseType:     constants.ProductPurchaseMember,
		FulfillmentType:  constants.FulfillmentTypeManual,
		ManualStockTotal: 0,
		IsActive:         true,
	}
	if err := db.Create(outOfStockProduct).Error; err != nil {
		t.Fatalf("create fallback product failed: %v", err)
	}

	stats, err := repo.GetStockStats(5)
	if err != nil {
		t.Fatalf("get stock stats failed: %v", err)
	}
	if stats.ManualAvailableUnits != 5 {
		t.Fatalf("manual available units want 5 got %d", stats.ManualAvailableUnits)
	}
	if stats.LowStockProducts != 1 {
		t.Fatalf("low stock products want 1 got %d", stats.LowStockProducts)
	}
	if stats.OutOfStockProducts != 1 {
		t.Fatalf("out of stock products want 1 got %d", stats.OutOfStockProducts)
	}
}

func TestGetInventoryAlertItemsFallsBackToProductLevelWhenOnlyInactiveAutoSKUHasStock(t *testing.T) {
	repo, db := setupDashboardRepositoryTest(t)
	if err := db.AutoMigrate(&cardsecretdomain.Secret{}); err != nil {
		t.Fatalf("migrate card secret failed: %v", err)
	}

	category := createDashboardCategory(t, db, "dashboard-auto-legacy-stock")
	product := &productdomain.Product{
		CategoryID:      category.ID,
		Slug:            "dashboard-auto-legacy-stock",
		TitleJSON:       jsonmap.JSON{"zh-CN": "自动发货商品"},
		PriceAmount:     money.FromDecimal(decimal.NewFromInt(99)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		IsActive:        true,
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	legacySKU := &productdomain.ProductSKU{
		ProductID:   product.ID,
		SKUCode:     productdomain.DefaultSKUCode,
		PriceAmount: money.FromDecimal(decimal.NewFromInt(99)),
		IsActive:    false,
		SortOrder:   0,
	}
	activeSKU := &productdomain.ProductSKU{
		ProductID:   product.ID,
		SKUCode:     "SKU-2",
		PriceAmount: money.FromDecimal(decimal.NewFromInt(99)),
		IsActive:    true,
		SortOrder:   1,
	}
	if err := db.Create(legacySKU).Error; err != nil {
		t.Fatalf("create legacy sku failed: %v", err)
	}
	if err := db.Model(legacySKU).Update("is_active", false).Error; err != nil {
		t.Fatalf("disable legacy sku failed: %v", err)
	}
	if err := db.Create(activeSKU).Error; err != nil {
		t.Fatalf("create active sku failed: %v", err)
	}

	for idx := 0; idx < 2; idx++ {
		row := &cardsecretdomain.Secret{
			ProductID: product.ID,
			SKUID:     legacySKU.ID,
			Secret:    fmt.Sprintf("LEGACY-%d", idx),
			Status:    cardsecretdomain.StatusAvailable,
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create legacy card secret failed: %v", err)
		}
	}

	rows, err := repo.GetInventoryAlertItems(5)
	if err != nil {
		t.Fatalf("get inventory alert items failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("inventory alert rows want 1 got %d: %+v", len(rows), rows)
	}
	if rows[0].SKUID != activeSKU.ID {
		t.Fatalf("fallback row should reuse the only active sku %d, got skuid=%d", activeSKU.ID, rows[0].SKUID)
	}
	if rows[0].AvailableStock != 2 {
		t.Fatalf("fallback row stock want 2 got %d", rows[0].AvailableStock)
	}
	if rows[0].AlertType != constants.NotificationAlertTypeLowStockProducts {
		t.Fatalf("fallback row alert type want low_stock_products got %s", rows[0].AlertType)
	}
}
