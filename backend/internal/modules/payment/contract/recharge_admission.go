package contract

import (
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	"time"
)

// RechargeAdmissionStore is implemented by the transaction-bound payment store.
// LockRechargeUser must run before any reads in the admission transaction.
type RechargeAdmissionStore interface {
	LockRechargeUser(uint) error
	PendingRechargePayments(uint, time.Time, time.Time) ([]paymentdomain.Payment, error)
}
