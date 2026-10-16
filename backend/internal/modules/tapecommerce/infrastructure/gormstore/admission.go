package gormstore

import (
	"context"
	"errors"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// Preparation pins a trusted buyer and wallet before the signing round trip.
// One invoice per order is intentional: expired signatures are never overwritten.
type Preparation struct {
	ShopID    string `gorm:"primaryKey;size:128"`
	OrderID   uint   `gorm:"primaryKey"`
	BuyerID   uint
	Guest     bool
	Payer     string
	CreatedAt time.Time
}

func (Preparation) TableName() string { return "tapecommerce_quote_preparations" }

type Binding struct {
	InvoiceID string `gorm:"primaryKey;size:66"`
	BuyerID   uint
	Guest     bool
	PaymentID uint `gorm:"uniqueIndex;not null"`
	Signature string
	CreatedAt time.Time
}

func (Binding) TableName() string { return "tapecommerce_payment_bindings" }

func (s *Store) Prepare(ctx context.Context, p Preparation) error {
	p.Payer = strings.ToLower(p.Payer)
	if p.ShopID == "" || p.OrderID == 0 || (p.BuyerID == 0 && !p.Guest) || (p.BuyerID != 0 && p.Guest) || !domain.ValidHex(p.Payer, 20) || domain.IsZeroHex(p.Payer) {
		return domain.ErrInvalid
	}
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&p)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 1 {
		return nil
	}
	old, err := s.Prepared(ctx, p.ShopID, p.OrderID)
	if err != nil {
		return err
	}
	if old.BuyerID != p.BuyerID || old.Guest != p.Guest || old.Payer != p.Payer {
		return domain.ErrConflict
	}
	return nil
}
func (s *Store) Prepared(ctx context.Context, shop string, orderID uint) (Preparation, error) {
	var p Preparation
	err := s.db.WithContext(ctx).Where("shop_id = ? AND order_id = ?", shop, orderID).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrNotFound
	}
	return p, err
}
func (s *Store) Bind(ctx context.Context, b Binding) error {
	if (b.BuyerID == 0 && !b.Guest) || (b.BuyerID != 0 && b.Guest) || b.PaymentID == 0 || !domain.ValidHex(b.InvoiceID, 32) || !domain.ValidHex(b.Signature, 65) {
		return domain.ErrInvalid
	}
	return s.db.WithContext(ctx).Create(&b).Error
}
func (s *Store) Binding(ctx context.Context, id string) (Binding, error) {
	var b Binding
	err := s.db.WithContext(ctx).Where("invoice_id = ?", strings.ToLower(id)).First(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrNotFound
	}
	return b, err
}

// LockAdmission is deliberately usable only on a caller-owned DB transaction.
// Lock order is order -> payment -> invoice -> event -> outbox -> stock. Review
// locks the same invoice row, so review-after-claim cannot race inventory release.
func (s *Store) LockAdmission(ctx context.Context, j domain.Job, now time.Time) (domain.Payment, error) {
	if _, ok := s.db.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return domain.Payment{}, domain.ErrDisabled
	}
	r, err := load(s.db.WithContext(ctx), j.ShopID, j.InvoiceID, true)
	if err != nil {
		return domain.Payment{}, err
	}
	if r.OrderID != j.OrderID || r.State != domain.FulfillmentPending || j.Key != "tapecommerce:"+r.InvoiceID {
		return domain.Payment{}, domain.ErrReview
	}
	p, err := s.Get(ctx, j.ShopID, j.InvoiceID)
	if err != nil {
		return p, err
	}
	if p.Evidence == nil || !p.Evidence.Safe || p.Evidence.PaidAt == 0 || p.Evidence.TxHash != p.TxHash || p.Evidence.SafeBlock < p.Evidence.BlockNumber {
		return p, domain.ErrReview
	}
	var o outboxRecord
	err = s.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("invoice_id = ?", r.InvoiceID).First(&o).Error
	if err != nil {
		return p, err
	}
	if o.State != "processing" || o.Key != j.Key || o.Lease == "" || o.Lease != j.Lease || !o.LeaseUntil.After(now) {
		return p, domain.ErrLease
	}
	return p, nil
}

// GetForUpdate serializes authorized delivery reads with Review. The caller
// must first hold the original order lock to share cancellation/refund order.
func (s *Store) GetForUpdate(ctx context.Context, shop, id string) (domain.Payment, error) {
	if _, ok := s.db.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return domain.Payment{}, domain.ErrDisabled
	}
	if _, err := load(s.db.WithContext(ctx), shop, id, true); err != nil {
		return domain.Payment{}, err
	}
	return s.Get(ctx, shop, id)
}
