package application

import (
	"time"

	chain "github.com/dujiao-next/internal/modules/payment/application/chainpayment"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
)

// Expiration calls this while holding payment then recharge locks. A queue error
// aborts the expiration transaction so the worker retries instead of forgetting
// the finite deadline. No payment/intent deadline is rewritten.
func (s *PaymentService) deferChainRechargeExpiry(tx paymentcontract.Transaction, p *paymentdomain.Payment, r *walletdomain.RechargeOrder, now time.Time) (bool, error) {
	if p == nil || r == nil || p.ProviderType != "selfhost" || r.Status != "pending" || p.OrderID != 0 {
		return false, nil
	}
	store := chain.Stores(tx)
	if store == nil {
		return false, nil
	}
	intent, err := store.Get(p.ID, false)
	if err != nil {
		return false, err
	}
	if intent == nil || intent.UserID != r.UserID {
		return false, nil
	}
	until, ok := paymentdomain.PendingChainConfirmationUntil(p, intent, now)
	if !ok {
		return false, nil
	}
	if s.queue != nil && s.queue.Enabled() {
		if err := s.queue.EnqueueWalletRechargeExpire(p.ID, until.Sub(now)); err != nil {
			return false, err
		}
	}
	return true, nil
}
