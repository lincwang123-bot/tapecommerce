// Package settlement contains transaction-owned payment admission use cases.
// It has no transport, database handle, signer, RPC, or card-allocation logic.
package settlement

import (
	"errors"
	"github.com/dujiao-next/internal/constants"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	"time"
)

var ErrIneligible = errors.New("verified_stock_payment_ineligible")

// ValidateOrder limits this use case to an original local-stock, member-owned,
// root order (including local-stock child orders) with no hybrid wallet, reseller,
// affiliate, refund or prior payment.
// The caller owns the order lock and checks the authorized buyer before calling.
func ValidateOrder(o *orderdomain.Order, buyer uint, amount string, paidAt time.Time) error {
	if o == nil || o.ParentID != nil || !eligible(o, buyer, amount, paidAt) {
		return ErrIneligible
	}
	if len(o.Children) == 0 {
		if len(o.Items) == 0 {
			return ErrIneligible
		}
		return nil
	}
	total := o.TotalAmount.Sub(o.TotalAmount.Decimal)
	for idx := range o.Children {
		child := &o.Children[idx]
		if child.ParentID == nil || *child.ParentID != o.ID || len(child.Children) > 0 || len(child.Items) == 0 || !eligible(child, buyer, child.TotalAmount.String(), paidAt) {
			return ErrIneligible
		}
		total = total.Add(child.TotalAmount.Decimal)
	}
	if !total.Equal(o.TotalAmount.Decimal) {
		return ErrIneligible
	}
	return nil
}
func eligible(o *orderdomain.Order, buyer uint, amount string, paidAt time.Time) bool {
	if buyer == 0 || o.UserID != buyer || o.ResellerID != nil || o.AffiliateProfileID != nil || o.DeletedAt != nil || o.CanceledAt != nil || o.PaidAt != nil || o.Status != constants.OrderStatusPendingPayment || o.Currency != "USDT" || !o.TotalAmount.IsPositive() || o.TotalAmount.String() != amount || !o.WalletPaidAmount.IsZero() || (!o.OnlinePaidAmount.IsZero() && !o.OnlinePaidAmount.Equal(o.TotalAmount.Decimal)) || !o.RefundedAmount.IsZero() || o.ExpiresAt == nil || paidAt.Unix() < o.CreatedAt.Unix() || !paidAt.Before(*o.ExpiresAt) {
		return false
	}
	for _, i := range o.Items {
		if i.ProductID == 0 || i.Quantity <= 0 || i.FulfillmentType != constants.FulfillmentTypeAuto || (i.SupplySource != "" && i.SupplySource != "local") {
			return false
		}
	}
	return true
}

// ApplyVerifiedStock requires a caller-owned transaction and already-locked
// order/payment. Its caller must verify the chain evidence and immutable quote
// under the same transaction before calling. Commit only after stock delivery.
func ApplyVerifiedStock(tx paymentcontract.Transaction, o *orderdomain.Order, p *paymentdomain.Payment, buyer uint, amount, providerRef string, paidAt, now time.Time) error {
	if tx == nil || ValidateOrder(o, buyer, amount, paidAt) != nil || p == nil || p.OrderID != o.ID || p.ProviderType != "tapecommerce" || p.ChannelType != "okb" || p.Status != constants.PaymentStatusPending || p.Currency != "USDT" || p.Amount.String() != amount || !p.FeeAmount.IsZero() || !p.FeeRate.IsZero() || !p.FixedFee.IsZero() || p.FeePolicy != "none" || p.SupersededAt != nil || p.ExpiredAt != nil || p.PaidAt != nil || p.DeletedAt != nil || p.ProviderRef != "" || providerRef == "" {
		return ErrIneligible
	}
	for _, id := range orderIDs(o) {
		payments, err := tx.Payments().ListByOrderID(id)
		if err != nil {
			return err
		}
		for _, other := range payments {
			if other.ID != p.ID && other.Status == constants.PaymentStatusSuccess {
				return ErrIneligible
			}
		}
	}
	// Retiring alternative pending payment links is part of the same commit.
	if _, err := tx.Payments().ExpirePendingByOrderIDs(orderIDs(o), now); err != nil {
		return err
	}
	p.Status = constants.PaymentStatusSuccess
	p.ProviderRef = providerRef
	p.PaidAt = &paidAt
	p.CallbackAt = &now
	p.UpdatedAt = now
	if err := tx.Payments().Update(p); err != nil {
		return err
	}
	if err := tx.Orders().UpdateFields(o.ID, map[string]interface{}{"status": constants.OrderStatusPaid, "paid_at": paidAt, "online_paid_amount": o.TotalAmount, "updated_at": now}); err != nil {
		return err
	}
	for _, child := range o.Children {
		if err := tx.Orders().UpdateFields(child.ID, map[string]interface{}{"status": constants.OrderStatusPaid, "paid_at": paidAt, "online_paid_amount": child.TotalAmount, "updated_at": now}); err != nil {
			return err
		}
	}
	return nil
}

func orderIDs(o *orderdomain.Order) []uint {
	ids := []uint{o.ID}
	for _, c := range o.Children {
		ids = append(ids, c.ID)
	}
	return ids
}
