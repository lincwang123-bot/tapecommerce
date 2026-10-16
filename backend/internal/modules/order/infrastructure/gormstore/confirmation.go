package gormstore

import (
	"errors"
	"time"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	"gorm.io/gorm"
)

// Called with the parent order row locked; payment creation and settlement use
// that same order lock. These reads do not invert the order -> payment lock order.
func (tx transaction) ChainConfirmationUntil(o *orderdomain.Order, now time.Time) (*time.Time, error) {
	if o == nil || o.CanceledAt != nil || o.PaidAt != nil || o.RefundedAmount.Decimal.IsPositive() || o.ExpiresAt == nil || o.Currency != "USDT" || !now.Before(o.ExpiresAt.Add(paymentdomain.ChainConfirmationGrace)) {
		return nil, nil
	}
	// Older installations and non-chain integration fixtures have no intent table.
	if !tx.db.Migrator().HasTable(&paymentdomain.ChainIntent{}) {
		return nil, nil
	}
	var payments []paymentdomain.Payment
	if err := tx.db.Where("order_id = ? AND provider_type = ? AND status = ?", o.ID, "selfhost", "pending").Find(&payments).Error; err != nil {
		return nil, err
	}
	var latest *time.Time
	for i := range payments {
		p := &payments[i]
		if p.Amount.Decimal.LessThan(o.TotalAmount.Decimal.Sub(o.WalletPaidAmount.Decimal)) {
			continue
		}
		var intent paymentdomain.ChainIntent
		if err := tx.db.First(&intent, "payment_id = ?", p.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if intent.CreatedAt.After(*o.ExpiresAt) || intent.ExpiresAt.After(*o.ExpiresAt) {
			continue
		}
		if until, ok := paymentdomain.PendingChainConfirmationUntil(p, &intent, now); ok && (latest == nil || until.After(*latest)) {
			copy := until
			latest = &copy
		}
	}
	return latest, nil
}
