package integrationtest

import (
	"errors"
	factorysupply "github.com/dujiao-next/internal/factorysupply"
	os "os"
	filepath "path/filepath"
	strconv "strconv"
	"testing"

	productwrite "github.com/dujiao-next/internal/modules/catalog/product/application/write"
	productcontract "github.com/dujiao-next/internal/modules/catalog/product/contract"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"

	categorydomain "github.com/dujiao-next/internal/modules/catalog/category/domain"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/shopspring/decimal"
)

func TestProductServiceCreateRejectsParentCategoryWithChildren(t *testing.T) {
	svc, db := newProductServiceForTest(t)

	parent := categorydomain.Category{
		Slug:     "games",
		NameJSON: jsonmap.JSON{"zh-CN": "games"},
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatalf("create parent category failed: %v", err)
	}
	child := categorydomain.Category{
		ParentID: parent.ID,
		Slug:     "steam",
		NameJSON: jsonmap.JSON{"zh-CN": "steam"},
	}
	if err := db.Create(&child).Error; err != nil {
		t.Fatalf("create child category failed: %v", err)
	}

	_, err := svc.Write.Create(productwrite.CreateProductInput{
		CategoryID:      parent.ID,
		Slug:            "invalid-parent-product",
		TitleJSON:       map[string]interface{}{"zh-CN": "invalid-parent-product"},
		PriceAmount:     decimal.NewFromInt(10),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeManual,
		ManualStockTotal: func() *int {
			value := 1
			return &value
		}(),
	})
	if err != productcontract.ErrProductCategoryInvalid {
		t.Fatalf("expected productcontract.ErrProductCategoryInvalid, got %v", err)
	}
}

func TestProductServiceCreateFiltersUnavailablePaymentChannels(t *testing.T) {
	svc, db := newProductServiceForTest(t)

	category := categorydomain.Category{
		Slug:     "payment-channel-category",
		NameJSON: jsonmap.JSON{"zh-CN": "payment-channel-category"},
	}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category failed: %v", err)
	}

	activeChannel := createProductTestPaymentChannel(t, db, "Active", true, false)
	inactiveChannel := createProductTestPaymentChannel(t, db, "Inactive", false, false)
	deletedChannel := createProductTestPaymentChannel(t, db, "Deleted", true, true)

	product, err := svc.Write.Create(productwrite.CreateProductInput{
		CategoryID:        category.ID,
		Slug:              "payment-channel-create",
		TitleJSON:         map[string]interface{}{"zh-CN": "payment-channel-create"},
		PriceAmount:       decimal.NewFromInt(10),
		PurchaseType:      constants.ProductPurchaseMember,
		FulfillmentType:   constants.FulfillmentTypeAuto,
		PaymentChannelIDs: []uint{deletedChannel.ID, inactiveChannel.ID, activeChannel.ID},
		IsActive: func() *bool {
			value := true
			return &value
		}(),
	})
	if err != nil {
		t.Fatalf("create product failed: %v", err)
	}

	got := productdomain.DecodePaymentChannelIDs(product.PaymentChannelIDs)
	if len(got) != 1 || got[0] != activeChannel.ID {
		t.Fatalf("expected only active payment channel %d, got %v", activeChannel.ID, got)
	}
}

func TestProductServiceCreateRejectsInvalidPurchaseLimits(t *testing.T) {
	svc, db := newProductServiceForTest(t)

	cat := categorydomain.Category{Slug: "test-purchase-limit", NameJSON: jsonmap.JSON{"zh-CN": "test"}}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}

	intPtr := func(v int) *int { return &v }

	_, err := svc.Write.Create(productwrite.CreateProductInput{
		CategoryID:          cat.ID,
		Slug:                "invalid-limit-product",
		TitleJSON:           map[string]interface{}{"zh-CN": "invalid-limit-product"},
		PriceAmount:         decimal.NewFromInt(10),
		PurchaseType:        constants.ProductPurchaseMember,
		FulfillmentType:     constants.FulfillmentTypeManual,
		ManualStockTotal:    intPtr(1),
		MinPurchaseQuantity: intPtr(10),
		MaxPurchaseQuantity: intPtr(5),
	})
	if !errors.Is(err, productcontract.ErrProductPurchaseLimitInvalid) {
		t.Fatalf("expected productcontract.ErrProductPurchaseLimitInvalid, got %v", err)
	}
}

func TestProductServiceCreateRollsBackProductAndSKUWhenWholesaleValidationFails(t *testing.T) {
	svc, db := newProductServiceForTest(t)
	category := categorydomain.Category{
		Slug:     "write-rollback-category",
		NameJSON: jsonmap.JSON{"zh-CN": "write-rollback-category"},
	}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}

	_, err := svc.Write.Create(productwrite.CreateProductInput{
		CategoryID:      category.ID,
		Slug:            "write-rollback-product",
		TitleJSON:       map[string]interface{}{"zh-CN": "write-rollback-product"},
		PriceAmount:     decimal.NewFromInt(10),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeAuto,
		WholesalePrices: &[]productdomain.WholesalePriceInput{{
			MinQuantity: 0,
			UnitPrice:   decimal.NewFromInt(8),
		}},
	})
	if !errors.Is(err, productdomain.ErrWholesalePriceInvalid) {
		t.Fatalf("expected productdomain.ErrWholesalePriceInvalid, got %v", err)
	}

	var productCount int64
	if err := db.Model(&productdomain.Product{}).Where("slug = ?", "write-rollback-product").Count(&productCount).Error; err != nil {
		t.Fatalf("count products: %v", err)
	}
	if productCount != 0 {
		t.Fatalf("transaction must roll back product, got %d rows", productCount)
	}
	var skuCount int64
	if err := db.Model(&productdomain.ProductSKU{}).Count(&skuCount).Error; err != nil {
		t.Fatalf("count product SKUs: %v", err)
	}
	if skuCount != 0 {
		t.Fatalf("transaction must roll back default SKU, got %d rows", skuCount)
	}
}

func TestFactorySupplyProductWritesCannotReclassifyReservedGoods(t *testing.T) {
	svc, db := newProductServiceForTest(t)
	category := categorydomain.Category{Slug: "supply-test", NameJSON: jsonmap.JSON{"zh-CN": "测试"}, IsActive: true}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policy, []byte(`{"schema_version":1,"shop_id":"shop_test","main_origin":"https://coco9.cc","reserved_brands":["ChatGPT","ExistingMainBrand"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_SHOP_ID", "shop_test")
	t.Setenv("FACTORY_SUPPLY_POLICY_FILE", policy)
	base := productwrite.CreateProductInput{SupplyBrand: "Independent", CategoryID: category.ID, Slug: "independent", TitleJSON: map[string]interface{}{"zh-CN": "自营商品"}, PriceAmount: decimal.NewFromInt(10), FulfillmentType: "auto"}
	for _, brand := range []string{"ChatGPT", "ＣｈａｔＧＰＴ", "ExistingMainBrand"} {
		bad := base
		bad.SupplyBrand = brand
		if _, err := svc.Write.Create(bad); !errors.Is(err, factorysupply.ErrMainSupplyRequired) {
			t.Fatalf("brand %q: %v", brand, err)
		}
	}
	local, err := svc.Write.Create(base)
	if err != nil {
		t.Fatal(err)
	}
	if local.SupplySource != factorysupply.Merchant || local.SupplyBrand != "Independent" {
		t.Fatalf("wrong identity: %+v", local)
	}
	id := strconv.FormatUint(uint64(local.ID), 10)
	bad := base
	bad.TitleJSON = map[string]interface{}{"en-US": "Chat-GPT Plus"}
	if _, err := svc.Write.Update(id, bad); !errors.Is(err, factorysupply.ErrMainSupplyRequired) {
		t.Fatal(err)
	}
	bad = base
	bad.SupplyBrand = "ChatGPT"
	if _, err := svc.Write.Update(id, bad); !errors.Is(err, factorysupply.ErrBrandRequired) {
		t.Fatal(err)
	}
	if _, err := svc.Admin.QuickUpdate(id, map[string]interface{}{"supply_source": "main"}); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	// Simulate an existing unmigrated product: batch activation must not skip identity checks.
	if err := db.Model(&productdomain.Product{}).Where("id = ?", local.ID).Updates(map[string]interface{}{"supply_source": "", "is_active": false}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Admin.QuickUpdate(id, map[string]interface{}{"is_active": true}); !errors.Is(err, factorysupply.ErrBrandRequired) {
		t.Fatal(err)
	}
	// A managed import remains managed after renaming. Delivery conversion is refused.
	if err := db.Model(&productdomain.Product{}).Where("id = ?", local.ID).Updates(map[string]interface{}{"supply_brand": "ChatGPT", "supply_source": "main", "fulfillment_type": "upstream"}).Error; err != nil {
		t.Fatal(err)
	}
	renamed := base
	renamed.SupplyBrand = "ChatGPT"
	renamed.FulfillmentType = "upstream"
	got, err := svc.Write.Update(id, renamed)
	if err != nil {
		t.Fatal(err)
	}
	if got.SupplySource != "main" || got.SupplyBrand != "ChatGPT" {
		t.Fatal("rename lost supply identity")
	}
	renamed.FulfillmentType = "manual"
	if _, err := svc.Write.Update(id, renamed); err == nil {
		t.Fatal("managed delivery converted")
	}
	if err := os.Remove(policy); err != nil {
		t.Fatal(err)
	}
	base.Slug = "policy-missing"
	if _, err := svc.Write.Create(base); !errors.Is(err, factorysupply.ErrPolicyUnavailable) {
		t.Fatal(err)
	}
	var count int64
	db.Model(&productdomain.Product{}).Count(&count)
	if count != 1 {
		t.Fatalf("rejected writes created %d products", count)
	}
}
