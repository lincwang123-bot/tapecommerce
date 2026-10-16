package gormstore

import (
	"context"
	"errors"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"gorm.io/gorm"
)

func (s *Store) ByOrder(ctx context.Context, shop string, orderID uint) (domain.Payment, error) {
	var r invoiceRecord
	err := s.db.WithContext(ctx).Where("shop_id = ? AND order_id = ?", shop, orderID).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Payment{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Payment{}, err
	}
	return s.Get(ctx, shop, r.InvoiceID)
}
func (s *Store) Pending(ctx context.Context, shop, after string, limit int) ([]string, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var ids []string
	err := s.db.WithContext(ctx).Model(&invoiceRecord{}).Where("shop_id = ? AND invoice_id > ? AND tx_hash <> '' AND state IN ?", shop, after, []string{domain.Submitted, domain.Detected, domain.FulfillmentPending}).Order("invoice_id ASC").Limit(limit).Pluck("invoice_id", &ids).Error
	return ids, err
}
func (s *Store) SchemaReady() bool {
	for _, model := range []any{&invoiceRecord{}, &eventRecord{}, &outboxRecord{}, &Preparation{}, &Binding{}} {
		if !s.db.Migrator().HasTable(model) {
			return false
		}
	}
	return s.db.Migrator().HasColumn(&Binding{}, "Guest") && s.db.Migrator().HasColumn(&Preparation{}, "Guest")
}
