package refund

import (
	"fmt"
	"github.com/dujiao-next/internal/voucherbridge"
	"strings"
	"time"

	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	"github.com/dujiao-next/internal/constants"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"

	"github.com/shopspring/decimal"
)

// AdminRefundToWalletInput describes an order refund whose settlement target
// is the registered user's wallet.
type AdminRefundToWalletInput struct {
	OrderID        uint
	IdempotencyKey string
	Amount         money.Amount
	Remark         string
}

// AdminRefundToWallet executes the order refund workflow and delegates only
// the account credit to Wallet. Order state, refund records, affiliate
// reversals, and reseller accounting remain owned by the order context.
func (s *Service) AdminRefundToWallet(
	input AdminRefundToWalletInput,
) (*orderdomain.Order, *walletdomain.Transaction, *orderdomain.OrderRefundRecord, error) {
	if input.OrderID == 0 {
		return nil, nil, nil, ErrOrderNotFound
	}
	amount := input.Amount.Decimal.Round(2)
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil, nil, nil, walletcontract.ErrInvalidAmount
	}
	if s == nil || s.wallets == nil {
		return nil, nil, nil, walletcontract.ErrAccountNotFound
	}

	requestKey, err := refundRequestKey(input.IdempotencyKey)
	if err != nil {
		return nil, nil, nil, err
	}
	remark := strings.TrimSpace(input.Remark)
	walletRemark := remark
	if walletRemark == "" {
		walletRemark = "管理员退款到余额"
	}
	reference := fmt.Sprintf("order:%d:admin_refund:%d", input.OrderID, time.Now().UnixNano())
	if requestKey != nil {
		reference = "order-refund:" + *requestKey
	}
	var (
		transactionResult  *walletdomain.Transaction
		refundRecordResult *orderdomain.OrderRefundRecord
	)

	config := settingsapp.DefaultOrderRefundConfig()
	if s.settingService != nil {
		loaded, err := s.settingService.GetOrderRefundConfig()
		if err != nil {
			return nil, nil, nil, err
		}
		config = loaded
	}

	err = s.orderStore.WithinTransaction(func(tx ordercontract.Transaction) error {
		orderRepository := tx.Orders()
		family, err := loadRefundFamily(orderRepository, input.OrderID)
		if err != nil {
			return err
		}
		order := *family.target
		existing, err := existingRefund(orderRepository, requestKey, input.OrderID, constants.OrderRefundTypeWallet, amount, remark, false)
		if err != nil {
			return err
		}
		if existing != nil {
			transactionResult, err = tx.Wallets().Wallets().GetTransactionByReference(reference)
			if err != nil {
				return err
			}
			if transactionResult == nil {
				return ErrOrderFetchFailed
			}
			refundRecordResult = existing
			return nil
		}
		if order.UserID == 0 {
			return walletcontract.ErrNotSupportedForGuest
		}
		if order.PaidAt == nil {
			return ErrOrderStatusInvalid
		}
		now := time.Now()
		if settingsapp.IsOrderRefundWindowExpired(order.CreatedAt, order.PaidAt, config.MaxRefundDays, now) {
			return ErrOrderRefundExpired
		}
		if order.TotalAmount.Decimal.LessThanOrEqual(decimal.Zero) {
			return ErrOrderStatusInvalid
		}
		refundedBefore := family.before[family.root.ID]
		refundable := family.refundable()
		if amount.GreaterThan(refundable) {
			return walletcontract.ErrRefundExceeded
		}

		if refs := orderdomain.UpstreamOrderRefs(&order); voucherbridge.Enabled() && len(refs) > 0 {
			if !amount.Equal(refundable) {
				return fmt.Errorf("voucher orders require a full remaining refund")
			}
			if err := voucherbridge.VoidOrders(refs); err != nil {
				return err
			}
		}
		_, transaction, err := s.wallets.CreditInTransaction(
			tx.Wallets(),
			walletcontract.CreditInput{
				UserID:    order.UserID,
				Amount:    money.FromDecimal(amount),
				Currency:  order.Currency,
				Type:      constants.WalletTxnTypeAdminRefund,
				Reference: reference,
				Remark:    walletRemark,
				OrderID:   &order.ID,
			},
		)
		if err != nil {
			return err
		}

		if err := family.apply(orderRepository, amount, now); err != nil {
			return err
		}
		if s.affiliateRefund != nil {
			if err := s.affiliateRefund.HandleOrderRefunded(
				tx.Affiliates(),
				family.root,
				amount,
				refundedBefore,
				"order_refunded_to_wallet",
			); err != nil {
				return err
			}
		}

		currency := strings.ToUpper(strings.TrimSpace(order.Currency))
		if currency == "" {
			currency = "CNY"
		}
		record := &orderdomain.OrderRefundRecord{
			RequestKey:               requestKey,
			UserID:                   order.UserID,
			GuestEmail:               order.GuestEmail,
			OrderID:                  order.ID,
			Type:                     constants.OrderRefundTypeWallet,
			Amount:                   money.FromDecimal(amount),
			PaymentFeeRefunded:       false,
			PaymentFeeRefundedAmount: money.FromDecimal(decimal.Zero),
			Currency:                 currency,
			Remark:                   remark,
			CreatedAt:                now,
			UpdatedAt:                now,
		}
		if err := orderRepository.CreateRefundRecord(record); err != nil {
			return ErrRefundRecordCreateFailed
		}
		if s.resellerAccounting != nil {
			if err := s.resellerAccounting.HandleRefundDeduct(tx.ResellerAccounting(), family.root, record, refundedBefore); err != nil {
				return err
			}
		}
		transactionResult = transaction
		refundRecordResult = record
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}

	order, err := s.orderStore.GetByID(input.OrderID)
	if err != nil {
		return nil, nil, nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, nil, nil, ErrOrderNotFound
	}
	return order, transactionResult, refundRecordResult, nil
}
