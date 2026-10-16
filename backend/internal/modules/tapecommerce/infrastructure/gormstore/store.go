package gormstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/modules/tapecommerce/contract"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type invoiceRecord struct {
	InvoiceID  string `gorm:"primaryKey;size:66"`
	ShopID     string `gorm:"size:128;not null;uniqueIndex:idx_tapecommerce_order,priority:1"`
	OrderID    uint   `gorm:"not null;uniqueIndex:idx_tapecommerce_order,priority:2"`
	ChainID    uint64
	Checkout   string
	TenantHash string
	Payer      string
	Merchant   string
	AmountWei  string
	Deadline   uint64
	FXSnapshot domain.FXSnapshot `gorm:"embedded;embeddedPrefix:fx_"`
	State      string
	TxHash     string
	Reason     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (invoiceRecord) TableName() string { return "tapecommerce_invoices" }

type eventRecord struct {
	Key         string `gorm:"primaryKey;size:160"`
	InvoiceID   string `gorm:"uniqueIndex;size:66;not null"`
	ChainID     uint64 `gorm:"uniqueIndex:idx_tapecommerce_chain_event,priority:1"`
	TxHash      string `gorm:"size:66;uniqueIndex:idx_tapecommerce_chain_event,priority:2"`
	LogIndex    uint64 `gorm:"uniqueIndex:idx_tapecommerce_chain_event,priority:3"`
	BlockNumber uint64
	BlockHash   string
	SafeBlock   uint64
	PaidAt      uint64
	Safe        bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (eventRecord) TableName() string { return "tapecommerce_payment_events" }

type outboxRecord struct {
	InvoiceID  string `gorm:"primaryKey;size:66"`
	Key        string `gorm:"uniqueIndex;size:160;not null"`
	State      string
	Lease      string
	LeaseUntil time.Time
	NextAt     time.Time
	Attempts   int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (outboxRecord) TableName() string { return "tapecommerce_fulfillment_outbox" }

type Store struct{ db *gorm.DB }

var _ contract.Store = (*Store)(nil)

func New(db *gorm.DB) *Store { return &Store{db: db} }

// Migrate is explicit and additive. The production registry does not call it.
// Run only on an approved shop DB when the full payment bridge is ready.
func (s *Store) Migrate(ctx context.Context) error {
	return s.db.WithContext(ctx).AutoMigrate(&invoiceRecord{}, &eventRecord{}, &outboxRecord{}, &Binding{}, &Preparation{})
}

func (s *Store) Create(ctx context.Context, invoice domain.Invoice) error {
	if err := invoice.Validate(); err != nil {
		return err
	}
	i := invoice.Normalize()
	r := invoiceRecord{InvoiceID: i.InvoiceID, ShopID: i.ShopID, OrderID: i.OrderID, ChainID: i.ChainID, Checkout: i.Checkout, TenantHash: i.TenantHash, Payer: i.Payer, Merchant: i.Merchant, AmountWei: i.AmountWei, Deadline: i.Deadline, FXSnapshot: i.FXSnapshot, State: domain.QuoteIssued}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	existing, err := s.Get(ctx, i.ShopID, i.InvoiceID)
	if err == nil && existing.Invoice == i {
		return nil
	}
	return domain.ErrConflict
}

func load(db *gorm.DB, shop, id string, locked bool) (invoiceRecord, error) {
	var r invoiceRecord
	query := db.Where("shop_id = ? AND invoice_id = ?", shop, strings.ToLower(id))
	if locked {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrNotFound
	}
	if err == nil {
		err = invoice(r).Validate()
	}
	return r, err
}

func invoice(r invoiceRecord) domain.Invoice {
	return domain.Invoice{InvoiceID: r.InvoiceID, ShopID: r.ShopID, OrderID: r.OrderID, ChainID: r.ChainID, Checkout: r.Checkout, TenantHash: r.TenantHash, Payer: r.Payer, Merchant: r.Merchant, AmountWei: r.AmountWei, Deadline: r.Deadline, FXSnapshot: r.FXSnapshot}
}

func (s *Store) Get(ctx context.Context, shop, id string) (domain.Payment, error) {
	db := s.db.WithContext(ctx)
	r, err := load(db, shop, id, false)
	if err != nil {
		return domain.Payment{}, err
	}
	p := domain.Payment{Invoice: invoice(r), State: r.State, TxHash: r.TxHash, Reason: r.Reason}
	var event eventRecord
	err = db.Where("invoice_id = ?", r.InvoiceID).First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.Evidence = &domain.Evidence{ChainID: event.ChainID, TxHash: event.TxHash, LogIndex: event.LogIndex, BlockNumber: event.BlockNumber, BlockHash: event.BlockHash, SafeBlock: event.SafeBlock, Safe: event.Safe, PaidAt: event.PaidAt}
	return p, nil
}

func (s *Store) Submit(ctx context.Context, shop, id, txHash string) error {
	if !domain.ValidHex(txHash, 32) {
		return domain.ErrInvalid
	}
	txHash = strings.ToLower(txHash)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := load(tx, shop, id, true)
		if err != nil {
			return err
		}
		if r.TxHash == txHash {
			return nil
		}
		if r.State != domain.QuoteIssued || r.TxHash != "" {
			return domain.ErrConflict
		}
		return tx.Model(&r).Updates(map[string]any{"tx_hash": txHash, "state": domain.Submitted}).Error
	})
}

func (s *Store) Record(ctx context.Context, shop, id string, evidence domain.Evidence) error {
	if evidence.ChainID != domain.ChainID || !domain.ValidHex(evidence.TxHash, 32) || !domain.ValidHex(evidence.BlockHash, 32) || (evidence.Safe && evidence.SafeBlock < evidence.BlockNumber) {
		return domain.ErrInvalid
	}
	evidence.TxHash = strings.ToLower(evidence.TxHash)
	evidence.BlockHash = strings.ToLower(evidence.BlockHash)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := load(tx, shop, id, true)
		if err != nil {
			return err
		}
		if r.State == domain.ReviewRequired {
			return domain.ErrReview
		}
		if r.TxHash != evidence.TxHash || r.State == domain.QuoteIssued {
			return domain.ErrConflict
		}
		key := fmt.Sprintf("%d:%s:%d", evidence.ChainID, evidence.TxHash, evidence.LogIndex)
		var old eventRecord
		err = tx.Where("invoice_id = ? OR key = ?", r.InvoiceID, key).First(&old).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && (old.InvoiceID != r.InvoiceID || old.Key != key || old.BlockHash != evidence.BlockHash || old.BlockNumber != evidence.BlockNumber || old.PaidAt != evidence.PaidAt) {
			return domain.ErrReorg
		}
		if err == nil && old.Safe && !evidence.Safe {
			return domain.ErrReorg
		}
		event := eventRecord{Key: key, InvoiceID: r.InvoiceID, ChainID: evidence.ChainID, TxHash: evidence.TxHash, LogIndex: evidence.LogIndex, BlockNumber: evidence.BlockNumber, BlockHash: evidence.BlockHash, SafeBlock: evidence.SafeBlock, Safe: evidence.Safe, PaidAt: evidence.PaidAt}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&event)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return domain.ErrConflict
			}
		} else if err := tx.Model(&old).Updates(map[string]any{"safe": evidence.Safe, "safe_block": evidence.SafeBlock}).Error; err != nil {
			return err
		}
		if r.State == domain.Delivered {
			return nil
		}
		state := domain.Detected
		if evidence.Safe {
			state = domain.FulfillmentPending
			job := outboxRecord{InvoiceID: r.InvoiceID, Key: "tapecommerce:" + r.InvoiceID, State: "pending"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; err != nil {
				return err
			}
		}
		return tx.Model(&r).Update("state", state).Error
	})
}

func (s *Store) Review(ctx context.Context, shop, id, reason string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := load(tx, shop, id, true)
		if err != nil {
			return err
		}
		if err := tx.Model(&r).Updates(map[string]any{"state": domain.ReviewRequired, "reason": reason}).Error; err != nil {
			return err
		}
		return tx.Model(&outboxRecord{}).Where("invoice_id = ? AND state != ?", r.InvoiceID, "done").Update("state", "review").Error
	})
}
