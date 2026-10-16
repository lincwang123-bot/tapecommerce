package gormstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) Claim(ctx context.Context, shop, id string, now time.Time, ttl time.Duration) (domain.Job, error) {
	var job domain.Job
	if ttl <= 0 || ttl > 10*time.Minute {
		return job, domain.ErrInvalid
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := load(tx, shop, id, true)
		if err != nil {
			return err
		}
		if r.State != domain.FulfillmentPending {
			return domain.ErrConflict
		}
		var outbox outboxRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("invoice_id = ?", r.InvoiceID).First(&outbox).Error; err != nil {
			return err
		}
		if outbox.State == "done" || outbox.State == "review" {
			return domain.ErrConflict
		}
		if outbox.State == "processing" && outbox.LeaseUntil.After(now) {
			return domain.ErrLease
		}
		if outbox.NextAt.After(now) {
			return domain.ErrPending
		}
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		lease := hex.EncodeToString(nonce[:])
		until := now.Add(ttl)
		attempts := outbox.Attempts + 1
		if err := tx.Model(&outbox).Updates(map[string]any{"state": "processing", "lease": lease, "lease_until": until, "attempts": attempts}).Error; err != nil {
			return err
		}
		job = domain.Job{InvoiceID: r.InvoiceID, ShopID: r.ShopID, OrderID: r.OrderID, Key: outbox.Key, Lease: lease, LeaseUntil: until, Attempts: attempts}
		return nil
	})
	return job, err
}

func (s *Store) Finish(ctx context.Context, job domain.Job, delivered bool, reason string, next time.Time) error {
	if job.Lease == "" {
		return domain.ErrLease
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := load(tx, job.ShopID, job.InvoiceID, true)
		if err != nil {
			return err
		}
		// Atomic admission may have committed both the effect and completion.
		if delivered && r.OrderID == job.OrderID && r.State == domain.Delivered {
			var done outboxRecord
			if err := tx.Where("invoice_id = ? AND key = ? AND state = ?", job.InvoiceID, job.Key, "done").First(&done).Error; err != nil {
				return domain.ErrConflict
			}
			return nil
		}
		if r.OrderID != job.OrderID || r.State != domain.FulfillmentPending {
			return domain.ErrConflict
		}
		state, invoiceState := "pending", domain.FulfillmentPending
		if delivered {
			state, invoiceState = "done", domain.Delivered
		} else if reason == "delivery_review_required" || job.Attempts >= 8 {
			state, invoiceState = "review", domain.ReviewRequired
		}
		res := tx.Model(&outboxRecord{}).Where("invoice_id = ? AND key = ? AND lease = ? AND state = ?", job.InvoiceID, job.Key, job.Lease, "processing").Updates(map[string]any{"state": state, "next_at": next, "lease": "", "lease_until": time.Time{}})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return domain.ErrLease
		}
		return tx.Model(&r).Updates(map[string]any{"state": invoiceState, "reason": reason}).Error
	})
}
