package producthttp

import (
	"encoding/json"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"testing"
)

func TestCNYRequestAndResponse(t *testing.T) {
	for _, raw := range []string{`{"price_cny_amount":48,"skus":[{"price_cny_amount":"128"}]}`, `{"price_cny_amount":"48","skus":[{"price_cny_amount":128}]}`} {
		var req CreateProductRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(err)
		}
		if req.PriceCNYAmount == nil || !req.PriceCNYAmount.Equal(decimal.NewFromInt(48)) || toProductSKUInputs(req.SKUs)[0].PriceCNYAmount.String() != "128" {
			t.Fatal("CNY request not mapped")
		}
	}
	var req CreateProductRequest
	if err := json.Unmarshal([]byte(`{"price_cny_amount":"nope"}`), &req); err == nil {
		t.Fatal("non-numeric CNY accepted")
	}
	p := productdomain.Product{PriceCNYAmount: money.FromDecimal(decimal.NewFromInt(128)), SKUs: []productdomain.ProductSKU{
		{ID: 1, IsActive: true, PriceCNYAmount: money.FromDecimal(decimal.NewFromInt(128)), PriceAmount: money.FromDecimal(decimal.RequireFromString("19.08"))},
		{ID: 2, IsActive: true, PriceCNYAmount: money.FromDecimal(decimal.NewFromInt(48)), PriceAmount: money.FromDecimal(decimal.RequireFromString("7.15"))},
	}}
	view, err := (&PublicHandler{}).decoratePublicProduct(&p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.PriceCNYAmount.String() != "48.00" || view.PriceAmount.String() != "7.15" || view.CNYPerUSDT != "6.71" || view.SKUs[0].PriceCNYAmount.String() != "128.00" {
		t.Fatalf("public DTO lost CNY: %+v", view)
	}
	for _, v := range []any{p, view} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		if object["cny_per_usdt"] != "6.71" || object["price_cny_amount"] == nil {
			t.Fatalf("missing JSON fields: %s", raw)
		}
	}
}
