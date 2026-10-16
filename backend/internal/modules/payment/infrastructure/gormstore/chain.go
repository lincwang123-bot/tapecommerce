package gormstore

import (
	"errors"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type chainStore struct{ db *gorm.DB }

func (s *Store) ChainPayments() contract.ChainStore      { return &chainStore{s.db} }
func (t transaction) ChainPayments() contract.ChainStore { return &chainStore{t.db} }
func (s *chainStore) Get(id uint, lock bool) (*domain.ChainIntent, error) {
	var v domain.ChainIntent
	q := s.db
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	e := q.First(&v, "payment_id = ?", id).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}
func (s *chainStore) Create(v *domain.ChainIntent) (bool, error) {
	r := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(v)
	return r.RowsAffected == 1, r.Error
}

// Only verification progress is mutable. No update path can release or alter a
// reserved amount, recipient, business owner, chain or invoice deadline.
func (s *chainStore) Update(v *domain.ChainIntent) error {
	return s.db.Model(&domain.ChainIntent{}).Where("payment_id = ?", v.PaymentID).Updates(map[string]any{"submitted_hash": v.SubmittedHash, "verification_status": v.VerificationStatus, "verification_code": v.VerificationCode, "received_units": v.ReceivedUnits, "next_check_at": v.NextCheckAt, "attempts": v.Attempts, "lease_token": v.LeaseToken, "lease_until": v.LeaseUntil, "updated_at": v.UpdatedAt}).Error
}
func (s *chainStore) ClaimReceipt(v *domain.ChainReceipt) (bool, error) {
	r := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(v)
	if r.Error != nil || r.RowsAffected == 1 {
		return r.RowsAffected == 1, r.Error
	}
	var old domain.ChainReceipt
	e := s.db.Where("network = ? AND tx_hash = ? AND log_index = ?", v.Network, v.TxHash, v.LogIndex).First(&old).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return e == nil && old.PaymentID == v.PaymentID && old.AmountUnits == v.AmountUnits && old.Recipient == v.Recipient, e
}
func (s *chainStore) Acquire(id uint, token string, now, until time.Time) (bool, error) {
	r := s.db.Model(&domain.ChainIntent{}).Where("payment_id = ? AND verification_status NOT IN ? AND (next_check_at IS NULL OR next_check_at <= ?) AND (lease_until IS NULL OR lease_until < ?)", id, []string{"verified", "review_required"}, now, now).Updates(map[string]any{"lease_token": token, "lease_until": until})
	return r.RowsAffected == 1, r.Error
}
func (s *chainStore) Due(now time.Time, limit int) ([]domain.ChainIntent, error) {
	var rows []domain.ChainIntent
	e := s.db.Where("submitted_hash <> '' AND verification_status IN ? AND next_check_at <= ? AND (lease_until IS NULL OR lease_until < ?)", []string{"pending", "unavailable"}, now, now).Order("next_check_at").Limit(limit).Find(&rows).Error
	return rows, e
}

// Reserve half the bounded batch for unexpired invoices.
// The remaining oldest-due lane guarantees old/outage-recovery work progresses
// even under sustained arrivals. Both lanes retain the same durable lease gate.
func (s *chainStore) DiscoveryDue(network string, now time.Time, limit int) ([]domain.ChainIntent, error) {
	if limit <= 0 {
		return nil, nil
	}
	base := func() *gorm.DB {
		return s.db.Where("network = ? AND verification_status NOT IN ? AND expires_at >= ? AND (discovery_next_at IS NULL OR discovery_next_at <= ?) AND (discovery_lease_until IS NULL OR discovery_lease_until < ?)", network, []string{"verified", "review_required"}, now.Add(-7*24*time.Hour), now, now).Order("COALESCE(discovery_next_at, created_at), payment_id")
	}
	var recent, rest []domain.ChainIntent
	if quota := limit / 2; quota > 0 {
		if err := base().Where("expires_at > ?", now).Limit(quota).Find(&recent).Error; err != nil {
			return nil, err
		}
	}
	q := base()
	if len(recent) > 0 {
		ids := make([]uint, len(recent))
		for i, v := range recent {
			ids[i] = v.PaymentID
		}
		q = q.Where("payment_id NOT IN ?", ids)
	}
	if err := q.Limit(limit - len(recent)).Find(&rest).Error; err != nil {
		return nil, err
	}
	return append(recent, rest...), nil
}
func (s *chainStore) AcquireDiscovery(id uint, token string, now, until time.Time) (bool, error) {
	r := s.db.Model(&domain.ChainIntent{}).Where("payment_id = ? AND verification_status NOT IN ? AND (discovery_next_at IS NULL OR discovery_next_at <= ?) AND (discovery_lease_until IS NULL OR discovery_lease_until < ?)", id, []string{"verified", "review_required"}, now, now).Updates(map[string]any{"discovery_lease": token, "discovery_lease_until": until})
	return r.RowsAffected == 1, r.Error
}
func (s *chainStore) FinishDiscovery(id uint, token, cursor, code string, next time.Time) error {
	return s.db.Model(&domain.ChainIntent{}).Where("payment_id = ? AND discovery_lease = ?", id, token).Updates(map[string]any{"discovery_cursor": cursor, "discovery_code": code, "discovery_next_at": next, "discovery_updated_at": time.Now(), "discovery_lease": "", "discovery_lease_until": nil}).Error
}
func (s *chainStore) AcquireCandidate(id uint, token string, now, until time.Time) (bool, error) {
	r := s.db.Model(&domain.ChainIntent{}).Where("payment_id = ? AND verification_status NOT IN ? AND (lease_until IS NULL OR lease_until < ?)", id, []string{"verified", "review_required"}, now).Updates(map[string]any{"lease_token": token, "lease_until": until})
	return r.RowsAffected == 1, r.Error
}
