package gormstore

import (
	"github.com/dujiao-next/internal/constants"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"gorm.io/gorm"
	"time"
)

// A no-op write locks the existing account identity across processes and also
// acquires SQLite's writer lock before the count snapshot. No wallet balance or
// user timestamps change. The lock lasts until the surrounding transaction ends.
func (s *Store) LockRechargeUser(id uint) error {
	r := s.db.Model(&userdomain.User{}).Where("id = ?", id).UpdateColumn("id", gorm.Expr("id"))
	if r.Error != nil {
		return r.Error
	}
	// MySQL may report zero changed rows for this intentional no-op. Verify
	// existence separately while retaining the acquired row lock.
	var user userdomain.User
	return s.db.Select("id").Where("id = ?", id).Take(&user).Error
}
func (s *Store) PendingRechargePayments(id uint, now, legacyCutoff time.Time) ([]paymentdomain.Payment, error) {
	var rows []paymentdomain.Payment
	err := s.db.Model(&paymentdomain.Payment{}).Select("payments.*").
		Joins("JOIN "+(walletdomain.RechargeOrder{}).TableName()+" r ON r.payment_id = payments.id").
		Where("r.user_id = ? AND r.status = ? AND payments.status IN ? AND (payments.expired_at > ? OR (payments.expired_at IS NULL AND payments.created_at > ?))", id, constants.WalletRechargeStatusPending, []string{constants.PaymentStatusInitiated, constants.PaymentStatusPending}, now, legacyCutoff).
		Order("payments.id DESC").Limit(100).Find(&rows).Error
	return rows, err
}
