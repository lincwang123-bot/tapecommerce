package application

import (
	"context"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/factorysupply"
	contract "github.com/dujiao-next/internal/modules/catalog/mapping/contract"
	domain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/dujiao-next/internal/upstream"
	"github.com/shopspring/decimal"
	"strconv"
	"time"
)

type DraftSKUBinding struct {
	LocalSKUID    uint `json:"local_sku_id"`
	UpstreamSKUID uint `json:"upstream_sku_id"`
}
type BindDraftInput struct {
	ConnectionID      uint              `json:"connection_id"`
	LocalProductID    uint              `json:"local_product_id"`
	UpstreamProductID uint              `json:"upstream_product_id"`
	SKUs              []DraftSKUBinding `json:"skus"`
}

// BindDraft preserves merchant content and prices. Only the delivery association is created.
func (s *Service) BindDraft(input BindDraftInput) (*domain.Mapping, error) {
	policy, err := factorysupply.Load()
	if err != nil {
		return nil, err
	}
	// Managed products are imported atomically; an arbitrary draft may not masquerade as one.
	if err := policy.MutableConnection(input.ConnectionID); err != nil {
		return nil, err
	}
	invalid := fmt.Errorf("只能绑定未上架且未映射的兑换码草稿；每个本地规格须选择有效的发码服务规格")
	product, err := s.products.GetByID(strconv.FormatUint(uint64(input.LocalProductID), 10))
	if err != nil {
		return nil, err
	}
	if product == nil || product.IsActive || product.IsMapped || product.FulfillmentType != constants.FulfillmentTypeUpstream {
		return nil, invalid
	}
	existing, err := s.mappings.GetByLocalProductID(product.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, contract.ErrMappingAlreadyExists
	}
	conn, err := s.connections.GetByID(input.ConnectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.Status != constants.ConnectionStatusActive {
		return nil, invalid
	}
	adapter, err := s.connections.GetAdapter(conn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	remote, err := adapter.GetProduct(ctx, input.UpstreamProductID)
	if err != nil {
		return nil, err
	}
	if remote == nil || !remote.IsActive {
		return nil, invalid
	}
	if _, _, err := policy.Import(input.ConnectionID, conn.BaseURL, input.UpstreamProductID, remote.Title, remote.Description, remote.Content, remote.Tags, remote.SKUs); err != nil {
		return nil, err
	}
	local, err := s.skus.ListByProduct(product.ID, false)
	if err != nil {
		return nil, err
	}
	if len(input.SKUs) == 0 || len(input.SKUs) != len(local) {
		return nil, invalid
	}
	available := map[uint]upstream.UpstreamSKU{}
	for _, sku := range remote.SKUs {
		if sku.IsActive {
			available[sku.ID] = sku
		}
	}
	owned := map[uint]bool{}
	for _, sku := range local {
		owned[sku.ID] = true
	}
	used := map[uint]bool{}
	for _, pair := range input.SKUs {
		if !owned[pair.LocalSKUID] || used[pair.UpstreamSKUID] {
			return nil, invalid
		}
		if _, ok := available[pair.UpstreamSKUID]; !ok {
			return nil, invalid
		}
		delete(owned, pair.LocalSKUID)
		used[pair.UpstreamSKUID] = true
	}
	mapping := &domain.Mapping{ConnectionID: conn.ID, LocalProductID: product.ID, UpstreamProductID: remote.ID, UpstreamFulfillmentType: remote.FulfillmentType, UpstreamStatus: domain.UpstreamStatusActive, IsActive: true}
	err = s.transactions.WithinTransaction(func(repos contract.ImportRepositories) error {
		// LocalProductID has a DB unique constraint, so concurrent retries cannot duplicate bindings.
		if err := repos.Mappings.Create(mapping); err != nil {
			return err
		}
		for _, pair := range input.SKUs {
			sku := available[pair.UpstreamSKUID]
			now := time.Now()
			row := &domain.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: pair.LocalSKUID, UpstreamSKUID: pair.UpstreamSKUID, UpstreamPrice: money.FromDecimal(decimal.Zero), UpstreamStock: sku.StockQuantity, UpstreamIsActive: true, StockSyncedAt: &now}
			if err := repos.SKUMappings.Create(row); err != nil {
				return err
			}
		}
		return repos.Products.QuickUpdate(strconv.FormatUint(uint64(product.ID), 10), map[string]interface{}{"is_mapped": true, "is_active": false, "fulfillment_type": constants.FulfillmentTypeUpstream})
	})
	if err != nil {
		return nil, err
	}
	return mapping, nil
}
