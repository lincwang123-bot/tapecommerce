package productwrite

import (
	productcontract "github.com/dujiao-next/internal/modules/catalog/product/contract"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	"github.com/shopspring/decimal"
)

// Omitted CNY preserves the previous denomination; explicit zero restores USDT pricing.
func resolveCatalogPrice(usdt decimal.Decimal, requestedCNY *decimal.Decimal, previousCNY decimal.Decimal) (decimal.Decimal, decimal.Decimal, error) {
	cny := previousCNY
	if requestedCNY != nil {
		cny = *requestedCNY
	}
	if cny.IsNegative() {
		return decimal.Zero, decimal.Zero, productcontract.ErrProductPriceInvalid
	}
	if cny.IsPositive() {
		cny = cny.Round(2)
		price := cny.Div(decimal.RequireFromString(productdomain.CNYPerUSDT)).Round(2)
		if !price.IsPositive() {
			return decimal.Zero, decimal.Zero, productcontract.ErrProductPriceInvalid
		}
		return price, cny, nil
	}
	return usdt.Round(2), decimal.Zero, nil
}

func minActivePriceCNY(skus []normalizedProductSKU) decimal.Decimal {
	var selected *normalizedProductSKU
	for i := range skus {
		sku := &skus[i]
		if sku.IsActive && (selected == nil || sku.PriceAmount.LessThan(selected.PriceAmount.Decimal)) {
			selected = sku
		}
	}
	if selected == nil {
		return decimal.Zero
	}
	return selected.PriceCNYAmount.Decimal
}
