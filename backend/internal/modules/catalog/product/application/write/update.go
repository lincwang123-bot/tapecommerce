package productwrite

import (
	"github.com/dujiao-next/internal/factorysupply"
	"strings"

	categorydomain "github.com/dujiao-next/internal/modules/catalog/category/domain"

	"github.com/dujiao-next/internal/constants"
	productcontract "github.com/dujiao-next/internal/modules/catalog/product/contract"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	"github.com/dujiao-next/internal/modules/catalog/product/manualform"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/jsonslice"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/shopspring/decimal"
)

// Update 更新商品
func (s *WriteService) Update(id string, input CreateProductInput) (*productdomain.Product, error) {
	product, err := s.products.GetByID(id)
	if err != nil {
		return nil, err
	}
	if product == nil {
		return nil, productcontract.ErrNotFound
	}
	policy, err := factorysupply.Load()
	if err != nil {
		return nil, err
	}
	if policy != nil {
		if err := factorysupply.LockedBrand(product.SupplyBrand, input.SupplyBrand); err != nil {
			return nil, err
		}
		if product.SupplyBrand == "" {
			product.SupplyBrand = input.SupplyBrand
		}
		if product.SupplySource == "" {
			product.SupplySource = factorysupply.Merchant
		}
		if err := policy.ValidateProduct(product.SupplyBrand, product.SupplySource, product.FulfillmentType, input.Slug, input.TitleJSON, input.DescriptionJSON, input.ContentJSON, input.InstructionsJSON, input.Tags, input.SKUs); err != nil {
			return nil, err
		}
	}
	if requested := strings.TrimSpace(input.FulfillmentType); requested != "" && requested != product.FulfillmentType {
		return nil, productcontract.ErrFulfillmentInvalid
	}
	priceAmount, priceCNYAmount, err := resolveCatalogPrice(input.PriceAmount, input.PriceCNYAmount, product.PriceCNYAmount.Decimal)
	if err != nil {
		return nil, err
	}
	if len(input.SKUs) == 0 && priceAmount.LessThanOrEqual(decimal.Zero) {
		return nil, productcontract.ErrProductPriceInvalid
	}
	if err := productdomain.ValidateCategoryAssignment(s.categories, input.CategoryID, product.CategoryID, productcontract.ErrProductCategoryInvalid); err != nil {
		return nil, err
	}

	count, err := s.products.CountBySlug(input.Slug, &id)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, productcontract.ErrSlugExists
	}

	product.CategoryID = input.CategoryID
	product.Category = categorydomain.Category{}
	product.Slug = input.Slug
	product.SeoMetaJSON = jsonmap.JSON(input.SeoMetaJSON)
	product.TitleJSON = jsonmap.JSON(input.TitleJSON)
	product.DescriptionJSON = jsonmap.JSON(input.DescriptionJSON)
	product.ContentJSON = jsonmap.JSON(input.ContentJSON)
	product.InstructionsJSON = jsonmap.JSON(input.InstructionsJSON)
	product.ManualFormSchemaJSON = jsonmap.JSON{}
	product.PriceAmount = money.FromDecimal(priceAmount)
	product.PriceCNYAmount = money.FromDecimal(priceCNYAmount)
	product.SortOrder = input.SortOrder
	product.Images = jsonslice.Strings(input.Images)
	product.Tags = jsonslice.Strings(input.Tags)
	paymentChannelIDs, err := s.filterAvailablePaymentChannelIDs(input.PaymentChannelIDs)
	if err != nil {
		return nil, err
	}
	product.PaymentChannelIDs = productdomain.EncodePaymentChannelIDs(paymentChannelIDs)
	if input.IsActive != nil {
		product.IsActive = *input.IsActive
	}
	if input.IsAffiliateEnabled != nil {
		product.IsAffiliateEnabled = *input.IsAffiliateEnabled
	}
	rawPurchaseType := strings.TrimSpace(input.PurchaseType)
	if rawPurchaseType == "" {
		rawPurchaseType = product.PurchaseType
	}
	purchaseType := productdomain.NormalizePurchaseType(rawPurchaseType)
	if purchaseType == "" {
		return nil, productcontract.ErrProductPurchaseInvalid
	}
	product.PurchaseType = purchaseType
	if input.MaxPurchaseQuantity != nil {
		product.MaxPurchaseQuantity = productdomain.NormalizePurchaseQuantityLimit(*input.MaxPurchaseQuantity)
	}
	if input.MinPurchaseQuantity != nil {
		product.MinPurchaseQuantity = productdomain.NormalizePurchaseQuantityLimit(*input.MinPurchaseQuantity)
	}
	if product.MinPurchaseQuantity > 0 && product.MaxPurchaseQuantity > 0 && product.MinPurchaseQuantity > product.MaxPurchaseQuantity {
		return nil, productcontract.ErrProductPurchaseLimitInvalid
	}
	stockDisplayMode := productdomain.NormalizeStockDisplayMode(input.StockDisplayMode)
	if stockDisplayMode == "" {
		return nil, productcontract.ErrProductStockDisplayInvalid
	}
	product.StockDisplayMode = stockDisplayMode
	rawFulfillmentType := strings.TrimSpace(input.FulfillmentType)
	if rawFulfillmentType == "" {
		rawFulfillmentType = product.FulfillmentType
	}
	fulfillmentType := productdomain.NormalizeFulfillmentType(rawFulfillmentType)
	if fulfillmentType == "" {
		return nil, productcontract.ErrFulfillmentInvalid
	}
	// Delivery mode is immutable: changing it can orphan stock or existing orders.
	if fulfillmentType != product.FulfillmentType {
		return nil, productcontract.ErrFulfillmentInvalid
	}
	product.FulfillmentType = fulfillmentType
	if fulfillmentType == constants.FulfillmentTypeManual {
		normalizedSchemaJSON, err := manualform.NormalizeSchema(jsonmap.JSON(input.ManualFormSchemaJSON))
		if err != nil {
			return nil, err
		}
		product.ManualFormSchemaJSON = normalizedSchemaJSON
	}

	manualStockTotal := product.ManualStockTotal
	if input.ManualStockTotal != nil {
		manualStockTotal = *input.ManualStockTotal
	}
	if manualStockTotal < constants.ManualStockUnlimited {
		return nil, productcontract.ErrManualStockInvalid
	}

	var normalizedSKUs []normalizedProductSKU
	if len(input.SKUs) > 0 {
		if s.skus == nil {
			return nil, productcontract.ErrProductSKUInvalid
		}
		existingSKUs, listErr := s.skus.ListByProduct(product.ID, false)
		if listErr != nil {
			return nil, listErr
		}
		existingSKUMap := make(map[uint]productdomain.ProductSKU, len(existingSKUs))
		for _, sku := range existingSKUs {
			existingSKUMap[sku.ID] = sku
		}
		var normalizeErr error
		normalizedSKUs, priceAmount, manualStockTotal, normalizeErr = s.normalizeProductSKUInputs(input.SKUs, fulfillmentType, existingSKUMap)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
	}

	product.PriceAmount = money.FromDecimal(priceAmount)
	product.PriceCNYAmount = money.FromDecimal(priceCNYAmount)
	if len(normalizedSKUs) > 0 {
		product.CostPriceAmount = money.FromDecimal(minActiveCostPrice(normalizedSKUs))
		product.PriceCNYAmount = money.FromDecimal(minActivePriceCNY(normalizedSKUs))
	} else {
		product.CostPriceAmount = money.FromDecimal(input.CostPriceAmount.Round(2))
	}
	product.ManualStockTotal = manualStockTotal

	if err := s.transactions.WithinTransaction(func(repositories TransactionRepositories) error {
		productRepo := repositories.Products
		skuRepo := repositories.SKUs
		cardSecretRepo := repositories.CardSecrets
		if len(normalizedSKUs) > 0 {
			if err := s.applyProductSKUsWithStockGuard(skuRepo, cardSecretRepo, product.ID, fulfillmentType, normalizedSKUs); err != nil {
				return err
			}
		} else if err := s.syncSingleProductSKU(skuRepo, product.ID, priceAmount, priceCNYAmount, product.CostPriceAmount.Decimal, product.ManualStockTotal, true); err != nil {
			return err
		}
		// 仅当请求显式携带批发价字段时才覆盖，省略字段（nil）保留原有配置，
		// 避免不关心批发价的局部更新静默清空已配阶梯。
		if input.WholesalePrices != nil {
			var skus []productdomain.ProductSKU
			if skuRepo != nil {
				var err error
				skus, err = skuRepo.ListByProduct(product.ID, false)
				if err != nil {
					return err
				}
			}
			wholesalePrices, err := productdomain.NormalizeWholesalePricesForSKUs(*input.WholesalePrices, skus)
			if err != nil {
				return err
			}
			product.WholesalePrices = wholesalePrices
		}
		if err := productRepo.Update(product); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.products.GetByID(id)
}
