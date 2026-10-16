package integrationtest

import (
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	categorydomain "github.com/dujiao-next/internal/modules/catalog/category/domain"
	productwrite "github.com/dujiao-next/internal/modules/catalog/product/application/write"
	productcontract "github.com/dujiao-next/internal/modules/catalog/product/contract"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/shopspring/decimal"
	"testing"
)

func cnyPointer(value string) *decimal.Decimal { d := decimal.RequireFromString(value); return &d }

func TestCNYCatalogCRUD(t *testing.T) {
	for _, tc := range []struct{ cny, usdt string }{{"48", "7.15"}, {"128", "19.08"}, {"700", "104.32"}, {"1180", "175.86"}} {
		t.Run(tc.cny, func(t *testing.T) {
			svc, db := newProductServiceForTest(t)
			category := categorydomain.Category{Slug: "cny", NameJSON: jsonmap.JSON{"zh-CN": "CNY"}}
			if err := db.Create(&category).Error; err != nil {
				t.Fatal(err)
			}
			input := productwrite.CreateProductInput{CategoryID: category.ID, Slug: "cny-product", TitleJSON: map[string]interface{}{"zh-CN": "CNY"}, PriceAmount: decimal.NewFromInt(999), PriceCNYAmount: cnyPointer(tc.cny), PurchaseType: constants.ProductPurchaseMember, FulfillmentType: constants.FulfillmentTypeManual}
			p, err := svc.Write.Create(input)
			if err != nil {
				t.Fatal(err)
			}
			assertPrices := func(wantCNY, wantUSDT string) {
				t.Helper()
				var stored productdomain.Product
				if err := db.First(&stored, p.ID).Error; err != nil {
					t.Fatal(err)
				}
				var sku productdomain.ProductSKU
				if err := db.Where("product_id = ?", p.ID).First(&sku).Error; err != nil {
					t.Fatal(err)
				}
				if stored.PriceAmount.String() != wantUSDT || sku.PriceAmount.String() != wantUSDT || stored.PriceCNYAmount.String() != wantCNY || sku.PriceCNYAmount.String() != wantCNY {
					t.Fatalf("product/SKU prices got %s/%s CNY %s/%s", stored.PriceAmount, sku.PriceAmount, stored.PriceCNYAmount, sku.PriceCNYAmount)
				}
			}
			assertPrices(decimal.RequireFromString(tc.cny).StringFixed(2), tc.usdt)
			input.PriceCNYAmount = nil
			if _, err := svc.Write.Update(fmt.Sprint(p.ID), input); err != nil {
				t.Fatal(err)
			}
			assertPrices(decimal.RequireFromString(tc.cny).StringFixed(2), tc.usdt)
			input.PriceCNYAmount = cnyPointer("0")
			input.PriceAmount = decimal.NewFromInt(15)
			if _, err := svc.Write.Update(fmt.Sprint(p.ID), input); err != nil {
				t.Fatal(err)
			}
			assertPrices("0.00", "15.00")
			input.PriceCNYAmount = cnyPointer("-0.001")
			if _, err := svc.Write.Update(fmt.Sprint(p.ID), input); !errors.Is(err, productcontract.ErrProductPriceInvalid) {
				t.Fatalf("negative CNY accepted: %v", err)
			}
			assertPrices("0.00", "15.00")
		})
	}
}

func TestCNYMultipleSKUPreservationAndMatching(t *testing.T) {
	svc, db := newProductServiceForTest(t)
	category := categorydomain.Category{Slug: "cny", NameJSON: jsonmap.JSON{"zh-CN": "CNY"}}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	inactive := false
	input := productwrite.CreateProductInput{CategoryID: category.ID, Slug: "cny-skus", TitleJSON: map[string]interface{}{"zh-CN": "CNY"}, PurchaseType: constants.ProductPurchaseMember, FulfillmentType: constants.FulfillmentTypeManual, SKUs: []productwrite.ProductSKUInput{
		{SKUCode: "expensive", PriceCNYAmount: cnyPointer("128")},
		{SKUCode: "cheap", PriceCNYAmount: cnyPointer("48")},
		{SKUCode: "inactive", PriceCNYAmount: cnyPointer("6.71"), IsActive: &inactive},
	}}
	p, err := svc.Write.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	if p.PriceAmount.String() != "7.15" || p.PriceCNYAmount.String() != "48.00" {
		t.Fatalf("unpaired minimum: %+v", p)
	}
	for i := range input.SKUs {
		input.SKUs[i].PriceCNYAmount = nil
		input.SKUs[i].PriceAmount = decimal.NewFromInt(999)
	}
	p, err = svc.Write.Update(fmt.Sprint(p.ID), input)
	if err != nil {
		t.Fatal(err)
	}
	if p.PriceAmount.String() != "7.15" || p.PriceCNYAmount.String() != "48.00" {
		t.Fatalf("omitted by-code CNY lost: %+v", p)
	}
	var skus []productdomain.ProductSKU
	if err := db.Where("product_id = ?", p.ID).Find(&skus).Error; err != nil {
		t.Fatal(err)
	}
	for i := range input.SKUs {
		for _, sku := range skus {
			if input.SKUs[i].SKUCode == sku.SKUCode {
				input.SKUs[i].ID = sku.ID
			}
		}
	}
	p, err = svc.Write.Update(fmt.Sprint(p.ID), input)
	if err != nil {
		t.Fatal(err)
	}
	if p.PriceCNYAmount.String() != "48.00" {
		t.Fatal("omitted by-ID CNY lost")
	}
}
