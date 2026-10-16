package refund

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

// refundFamily holds a locked root and its children. Root.RefundedAmount is the
// aggregate, child amounts are allocations of that same total, never extra money.
type refundFamily struct {
	root   *orderdomain.Order
	target *orderdomain.Order
	before map[uint]decimal.Decimal
}

func loadRefundFamily(orders ordercontract.Store, orderID uint) (*refundFamily, error) {
	root, err := orders.GetRootForRefundUpdate(orderID)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, ErrOrderNotFound
	}
	if root.ParentID != nil && *root.ParentID != 0 {
		return nil, ErrOrderStatusInvalid
	}
	family := &refundFamily{root: root, before: map[uint]decimal.Decimal{}}
	if root.ID == orderID {
		family.target = root
	}
	sort.Slice(root.Children, func(i, j int) bool { return root.Children[i].ID < root.Children[j].ID })
	ids := []uint{root.ID}
	for i := range root.Children {
		child := &root.Children[i]
		if child.ID == orderID {
			family.target = child
		}
		if child.UserID != root.UserID || !strings.EqualFold(child.Currency, root.Currency) {
			return nil, ErrOrderStatusInvalid
		}
		ids = append(ids, child.ID)
	}
	if family.target == nil {
		return nil, ErrOrderNotFound
	}
	records, err := orders.ListRefundRecordsByOrderIDs(ids)
	if err != nil {
		return nil, err
	}
	recorded := map[uint]decimal.Decimal{}
	recordedTotal := decimal.Zero
	for _, record := range records {
		amount := record.Amount.Decimal.Round(2)
		if amount.IsNegative() {
			return nil, ErrOrderStatusInvalid
		}
		recorded[record.OrderID] = recorded[record.OrderID].Add(amount)
		recordedTotal = recordedTotal.Add(amount)
	}
	// Older releases updated either the root or the child but not both. Take the
	// strongest durable evidence; never lower an amount or rewrite old records.
	rootAmount := decimal.Max(root.RefundedAmount.Decimal.Round(2), recordedTotal)
	childSum, totalChildren := decimal.Zero, decimal.Zero
	for i := range root.Children {
		child := &root.Children[i]
		amount := decimal.Max(child.RefundedAmount.Decimal.Round(2), recorded[child.ID])
		if amount.IsNegative() || amount.GreaterThan(child.TotalAmount.Decimal.Round(2)) {
			return nil, walletcontract.ErrRefundExceeded
		}
		child.RefundedAmount = money.FromDecimal(amount)
		childSum = childSum.Add(amount)
		totalChildren = totalChildren.Add(child.TotalAmount.Decimal.Round(2))
	}
	// Missing ledger evidence cannot distinguish legacy independent parent/child
	// counters from the new shared aggregate. Require reconciliation instead of
	// guessing and allowing another financial write.
	rowRoot := root.RefundedAmount.Decimal.Round(2)
	if recordedTotal.LessThan(decimal.Max(rowRoot, childSum)) {
		return nil, ErrOrderStatusInvalid
	}
	rootAmount = decimal.Max(rootAmount, childSum)
	if rootAmount.IsNegative() || rootAmount.GreaterThan(root.TotalAmount.Decimal.Round(2)) {
		return nil, walletcontract.ErrRefundExceeded
	}
	if len(root.Children) > 0 {
		if !totalChildren.Equal(root.TotalAmount.Decimal.Round(2)) {
			return nil, ErrOrderStatusInvalid
		}
		if err := allocateChildren(root, rootAmount.Sub(childSum)); err != nil {
			return nil, err
		}
	}
	root.RefundedAmount = money.FromDecimal(rootAmount)
	family.before[root.ID] = rootAmount
	for i := range root.Children {
		family.before[root.Children[i].ID] = root.Children[i].RefundedAmount.Decimal
	}
	return family, nil
}

// allocateChildren distributes a parent refund in proportion to each child's
// remaining refundable amount. Stable ID order and a last-child remainder keep
// every cent accounted for without exceeding any child's paid principal.
func allocateChildren(root *orderdomain.Order, amount decimal.Decimal) error {
	if amount.IsZero() {
		return nil
	}
	remaining := decimal.Zero
	for _, child := range root.Children {
		remaining = remaining.Add(child.TotalAmount.Decimal.Sub(child.RefundedAmount.Decimal))
	}
	if amount.IsNegative() || amount.GreaterThan(remaining) {
		return walletcontract.ErrRefundExceeded
	}
	left := amount
	for i := range root.Children {
		child := &root.Children[i]
		capacity := child.TotalAmount.Decimal.Sub(child.RefundedAmount.Decimal).Round(2)
		if capacity.LessThanOrEqual(decimal.Zero) {
			continue
		}
		allocation := left
		if capacity.LessThan(remaining) {
			allocation = left.Mul(capacity).Div(remaining).Round(2)
		}
		allocation = decimal.Min(allocation, capacity)
		child.RefundedAmount = money.FromDecimal(child.RefundedAmount.Decimal.Add(allocation))
		left = left.Sub(allocation)
		remaining = remaining.Sub(capacity)
	}
	if !left.IsZero() {
		return ErrOrderUpdateFailed
	}
	return nil
}

func (f *refundFamily) refundable() decimal.Decimal {
	return decimal.Min(f.target.TotalAmount.Decimal.Sub(f.target.RefundedAmount.Decimal), f.root.TotalAmount.Decimal.Sub(f.root.RefundedAmount.Decimal)).Round(2)
}

func (f *refundFamily) apply(orders ordercontract.Store, amount decimal.Decimal, now time.Time) error {
	if amount.GreaterThan(f.refundable()) {
		return walletcontract.ErrRefundExceeded
	}
	if f.target.ID == f.root.ID {
		if len(f.root.Children) > 0 {
			if err := allocateChildren(f.root, amount); err != nil {
				return err
			}
		}
	} else {
		f.target.RefundedAmount = money.FromDecimal(f.target.RefundedAmount.Decimal.Add(amount))
	}
	f.root.RefundedAmount = money.FromDecimal(f.root.RefundedAmount.Decimal.Add(amount))
	all := []*orderdomain.Order{f.root}
	for i := range f.root.Children {
		all = append(all, &f.root.Children[i])
	}
	for _, order := range all {
		amount := order.RefundedAmount.Decimal.Round(2)
		updates := map[string]interface{}{"refunded_amount": money.FromDecimal(amount), "updated_at": now}
		if amount.GreaterThan(decimal.Zero) {
			status := constants.OrderStatusPartiallyRefunded
			if amount.GreaterThanOrEqual(order.TotalAmount.Decimal.Round(2)) {
				status = constants.OrderStatusRefunded
			}
			updates["status"] = status
		}
		if err := orders.UpdateFields(order.ID, updates); err != nil {
			return ErrOrderUpdateFailed
		}
	}
	return nil
}

func refundRequestKey(raw string) (*string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) < 8 || len(raw) > 128 {
		return nil, walletcontract.ErrInvalidAmount
	}
	for _, ch := range raw {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("._:-", ch)) {
			return nil, walletcontract.ErrInvalidAmount
		}
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("admin-order-refund:"+raw)))
	return &hash, nil
}

func existingRefund(orders ordercontract.Store, key *string, orderID uint, kind string, amount decimal.Decimal, remark string, fee bool) (*orderdomain.OrderRefundRecord, error) {
	if key == nil {
		return nil, nil
	}
	record, err := orders.GetRefundRecordByRequestKey(*key)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	if record.OrderID != orderID || record.Type != kind || !record.Amount.Decimal.Equal(amount) || record.Remark != remark || record.PaymentFeeRefunded != fee {
		return nil, walletcontract.ErrInvalidAmount
	}
	return record, nil
}
