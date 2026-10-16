package catalogmappingbootstrap

import (
	"encoding/json"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/upstream"
	"net/http"
	"testing"
)

func TestCNYCatalogResistsUpstreamPriceChanges(t *testing.T) {
	svc, db, mapping, cleanup := setupMappingWithUpstreamHandler(t, "file:cny_sync_protection?mode=memory&cache=shared", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": upstream.UpstreamProduct{ID: 101, Title: jsonmap.JSON{"zh-CN": "P"}, PriceAmount: "500.00", IsActive: true, SKUs: []upstream.UpstreamSKU{{ID: 201, SKUCode: "SKU-A", PriceAmount: "500.00", IsActive: true, StockQuantity: 100}}}})
	})
	defer cleanup()
	if err := db.Model(&siteconnectiondomain.Connection{}).Where("id = ?", mapping.ConnectionID).Updates(map[string]any{"auto_sync_price": true, "exchange_rate": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&productdomain.ProductSKU{}).Where("product_id = ?", mapping.LocalProductID).Updates(map[string]any{"price_cny_amount": "48.00", "price_amount": "7.15"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&mappingdomain.SKUMapping{}).Where("product_mapping_id = ?", mapping.ID).Update("upstream_price", "500.00").Error; err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		var sku productdomain.ProductSKU
		db.Where("product_id = ?", mapping.LocalProductID).First(&sku)
		var p productdomain.Product
		db.First(&p, mapping.LocalProductID)
		if sku.PriceAmount.String() != "7.15" || sku.PriceCNYAmount.String() != "48.00" || p.PriceAmount.String() != "7.15" || p.PriceCNYAmount.String() != "48.00" {
			t.Fatalf("CNY overwritten: SKU %s/%s product %s/%s", sku.PriceAmount, sku.PriceCNYAmount, p.PriceAmount, p.PriceCNYAmount)
		}
	}
	if _, err := svc.ReapplyMarkup(mapping.ConnectionID); err != nil {
		t.Fatal(err)
	}
	check()
	if err := svc.SyncProduct(mapping.ID); err != nil {
		t.Fatal(err)
	}
	check()
}
