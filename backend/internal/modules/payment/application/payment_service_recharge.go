package application

import (
	"context"
	"errors"
	"github.com/dujiao-next/internal/voucherbridge"
	"strings"
	"time"

	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"

	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/dujiao-next/internal/shared/serial"

	"github.com/shopspring/decimal"
)

var ErrRechargeLimit = errors.New("请先完成或等待已有充值单过期，最多同时保留3笔待付款充值")
var ErrRechargePreparing = errors.New("相同充值正在生成，请稍后重试")

// CreateWalletRechargePaymentInput 创建钱包充值支付请求
type CreateWalletRechargePaymentInput struct {
	UserID        uint
	ChannelID     uint
	Amount        money.Amount
	Currency      string
	Remark        string
	ClientIP      string
	Context       context.Context
	RequestScheme string
}

// CreateWalletRechargePaymentResult 创建钱包充值支付结果
type CreateWalletRechargePaymentResult struct {
	Recharge *walletdomain.RechargeOrder
	Payment  *paymentdomain.Payment
}

// CreateWalletRechargePayment 创建钱包充值支付单
func (s *PaymentService) CreateWalletRechargePayment(input CreateWalletRechargePaymentInput) (*CreateWalletRechargePaymentResult, error) {
	if input.UserID == 0 || input.ChannelID == 0 {
		return nil, ErrPaymentInvalid
	}
	amount := input.Amount.Decimal.Round(2)
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil, walletcontract.ErrInvalidAmount
	}
	if s.walletRepo == nil {
		return nil, ErrPaymentCreateFailed
	}

	channel, err := s.channelRepo.GetByID(input.ChannelID)
	if err != nil {
		return nil, err
	}
	if channel == nil {
		return nil, ErrPaymentChannelNotFound
	}
	if !channel.IsActive {
		return nil, ErrPaymentChannelInactive
	}
	if s.userRepo == nil {
		return nil, ErrPaymentInvalid
	}
	user, err := s.userRepo.GetByID(input.UserID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if err := validateWalletChannelEligibility(*channel, user); err != nil {
		return nil, err
	}

	// 校验钱包充值是否允许该支付渠道
	if err := s.validateWalletRechargeChannel(channel.ID); err != nil {
		return nil, err
	}

	feeRate := channel.FeeRate.Decimal.Round(2)
	if feeRate.LessThan(decimal.Zero) || feeRate.GreaterThan(decimal.NewFromInt(100)) {
		return nil, ErrPaymentChannelConfigInvalid
	}
	fixedFee := channel.FixedFee.Decimal.Round(2)
	if fixedFee.LessThan(decimal.Zero) || fixedFee.GreaterThanOrEqual(decimal.NewFromInt(10000)) {
		return nil, ErrPaymentChannelConfigInvalid
	}
	if err := validatePaymentAmountForChannel(amount, channel); err != nil {
		return nil, err
	}

	feeConfig := settingsapp.DefaultPaymentFeeConfig()
	if s.settingService != nil {
		feeConfig = s.settingService.GetPaymentFeeConfig()
	}
	paymentAmount, feeAmount, feePolicy := calculatePaymentAmounts(amount, feeRate, fixedFee, feeConfig.CustomerFeeEnabled)
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		currency = "CNY"
	}
	if err := validatePaymentCurrencyForChannel(currency, channel); err != nil {
		return nil, err
	}
	if voucherbridge.Enabled() && (currency != "USDT" || !s.isStoreCryptoChannel(channel)) {
		return nil, ErrPaymentCurrencyMismatch
	}
	if shouldUseCNYPaymentCurrency(channel) {
		currency = "CNY"
	}
	now := time.Now()
	expires := now.Add(time.Duration(s.resolveExpireMinutes()) * time.Minute)
	reused := false
	placeholderExpires := expires
	if channel.ProviderType == "selfhost" && placeholderExpires.After(now.Add(2*time.Minute)) {
		placeholderExpires = now.Add(2 * time.Minute)
	}

	var payment *paymentdomain.Payment
	var recharge *walletdomain.RechargeOrder
	err = s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		gate, ok := tx.Payments().(paymentcontract.RechargeAdmissionStore)
		if !ok {
			return ErrPaymentCreateFailed
		}
		if err := gate.LockRechargeUser(input.UserID); err != nil {
			return err
		}
		pending, err := gate.PendingRechargePayments(input.UserID, time.Now(), now.Add(now.Sub(expires)))
		if err != nil {
			return err
		}
		for i := range pending {
			p := &pending[i]
			if p.ExceptionCode != "" || p.ChannelID != channel.ID || p.Currency != currency || !p.Amount.Decimal.Equal(paymentAmount) {
				continue
			}
			r, err := tx.Wallets().Wallets().GetRechargeOrderByPaymentID(p.ID)
			if err != nil {
				return err
			}
			if r == nil || !r.Amount.Decimal.Equal(amount) {
				continue
			}
			if !hasProviderResult(p) {
				return ErrRechargePreparing
			}
			payment, recharge, reused = p, r, true
			return nil
		}
		if len(pending) >= 3 {
			return ErrRechargeLimit
		}
		rechargeNo := generateWalletRechargeNo()
		paymentRepo := tx.Payments()
		payment = &paymentdomain.Payment{
			OrderID:         0,
			ChannelID:       channel.ID,
			ProviderType:    channel.ProviderType,
			ChannelType:     channel.ChannelType,
			InteractionMode: channel.InteractionMode,
			Amount:          money.FromDecimal(paymentAmount),
			FeeRate:         money.FromDecimal(feeRate),
			FixedFee:        money.FromDecimal(fixedFee),
			FeeAmount:       money.FromDecimal(feeAmount),
			FeePolicy:       feePolicy,
			Currency:        currency,
			Status:          constants.PaymentStatusInitiated,
			CreatedAt:       now,
			UpdatedAt:       now,
			ExpiredAt:       &placeholderExpires,
		}
		if err := paymentRepo.Create(payment); err != nil {
			return ErrPaymentCreateFailed
		}

		rechargeRepo := tx.Wallets().Wallets()
		remark := strings.TrimSpace(input.Remark)
		if remark == "" {
			remark = "余额充值"
		}
		recharge = &walletdomain.RechargeOrder{
			RechargeNo:      rechargeNo,
			UserID:          input.UserID,
			PaymentID:       payment.ID,
			ChannelID:       channel.ID,
			ProviderType:    channel.ProviderType,
			ChannelType:     channel.ChannelType,
			InteractionMode: channel.InteractionMode,
			Amount:          money.FromDecimal(amount),
			PayableAmount:   money.FromDecimal(paymentAmount),
			FeeRate:         money.FromDecimal(feeRate),
			FeeAmount:       money.FromDecimal(feeAmount),
			Currency:        currency,
			Status:          constants.WalletRechargeStatusPending,
			Remark:          remark,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := rechargeRepo.CreateRechargeOrder(recharge); err != nil {
			return ErrPaymentCreateFailed
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if payment == nil || recharge == nil {
		return nil, ErrPaymentCreateFailed
	}

	if reused {
		return &CreateWalletRechargePaymentResult{Payment: payment, Recharge: recharge}, nil
	}
	if payment.ProviderType == "selfhost" {
		// The committed initiated row consumes quota before any network request.
		// A crashed preparer therefore cannot admit unlimited parallel invoices.
		prepared, prepErr := s.prepareChain(input.Context, input.ChannelID)
		if prepErr == nil {
			prepErr = s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
				if err := s.reserveChain(tx, payment, channel, input.UserID, expires, prepared); err != nil {
					return err
				}
				// Enqueue before committing the reserved invoice: if the queue is
				// unavailable, no discoverable payable intent escapes this transaction.
				if s.queue != nil && s.queue.Enabled() {
					if err := s.queue.EnqueueWalletRechargeExpire(payment.ID, time.Until(*payment.ExpiredAt)); err != nil {
						return ErrQueueUnavailable
					}
				}
				return nil
			})
		}
		if prepErr != nil {
			cleanupErr := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
				payment.Status = constants.PaymentStatusFailed
				recharge.Status = constants.WalletRechargeStatusFailed
				if err := tx.Payments().Update(payment); err != nil {
					return err
				}
				return tx.Wallets().Wallets().UpdateRechargeOrder(recharge)
			})
			if cleanupErr != nil {
				return nil, ErrPaymentUpdateFailed
			}
			return nil, prepErr
		}
		return &CreateWalletRechargePaymentResult{Payment: payment, Recharge: recharge}, nil
	}
	// 复用支付网关下单逻辑，使用充值单号作为业务单号。
	virtualOrder := &orderdomain.Order{
		OrderNo: recharge.RechargeNo,
		UserID:  recharge.UserID,
	}
	if err := s.applyProviderPayment(CreatePaymentInput{
		ChannelID:        input.ChannelID,
		ClientIP:         input.ClientIP,
		Context:          input.Context,
		ReturnBizType:    "recharge",
		ReturnBusinessNo: recharge.RechargeNo,
		RequestScheme:    input.RequestScheme,
	}, virtualOrder, channel, payment); err != nil {
		_ = s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
			rechargeRepo := tx.Wallets().Wallets()
			paymentRepo := tx.Payments()
			failedAt := time.Now()
			payment.Status = constants.PaymentStatusFailed
			payment.UpdatedAt = failedAt
			if updateErr := paymentRepo.Update(payment); updateErr != nil {
				return updateErr
			}
			lockedRecharge, getErr := rechargeRepo.GetRechargeOrderByPaymentIDForUpdate(payment.ID)
			if getErr != nil || lockedRecharge == nil {
				return getErr
			}
			lockedRecharge.Status = constants.WalletRechargeStatusFailed
			lockedRecharge.UpdatedAt = failedAt
			return rechargeRepo.UpdateRechargeOrder(lockedRecharge)
		})
		return nil, err
	}
	if s.queue != nil && s.queue.Enabled() {
		delay := time.Duration(s.resolveExpireMinutes()) * time.Minute
		if err := s.queue.EnqueueWalletRechargeExpire(payment.ID, delay); err != nil {
			logger.Errorw("wallet_recharge_enqueue_timeout_expire_failed",
				"payment_id", payment.ID,
				"recharge_no", recharge.RechargeNo,
				"delay_minutes", int(delay/time.Minute),
				"error", err,
			)
			_ = s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
				rechargeRepo := tx.Wallets().Wallets()
				paymentRepo := tx.Payments()
				failedAt := time.Now()
				payment.Status = constants.PaymentStatusFailed
				payment.UpdatedAt = failedAt
				if updateErr := paymentRepo.Update(payment); updateErr != nil {
					return updateErr
				}
				lockedRecharge, getErr := rechargeRepo.GetRechargeOrderByPaymentIDForUpdate(payment.ID)
				if getErr != nil || lockedRecharge == nil {
					return getErr
				}
				if lockedRecharge.Status == constants.WalletRechargeStatusSuccess {
					return nil
				}
				lockedRecharge.Status = constants.WalletRechargeStatusFailed
				lockedRecharge.UpdatedAt = failedAt
				return rechargeRepo.UpdateRechargeOrder(lockedRecharge)
			})
			return nil, ErrQueueUnavailable
		}
	}

	reloadedRecharge, err := s.walletRepo.GetRechargeOrderByPaymentID(payment.ID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if reloadedRecharge != nil {
		recharge = reloadedRecharge
	}
	return &CreateWalletRechargePaymentResult{
		Recharge: recharge,
		Payment:  payment,
	}, nil
}

func generateWalletRechargeNo() string {
	return serial.Generate("WR")
}

// ExpireWalletRechargePayment 将未支付的钱包充值单标记为过期（幂等）。
func (s *PaymentService) ExpireWalletRechargePayment(paymentID uint) (*paymentdomain.Payment, error) {
	if paymentID == 0 {
		return nil, ErrPaymentInvalid
	}
	if s == nil || s.paymentRepo == nil || s.walletRepo == nil {
		return nil, ErrPaymentUpdateFailed
	}

	var output *paymentdomain.Payment
	err := s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		locked, err := tx.Payments().GetByIDForUpdate(paymentID)
		if err != nil {
			return ErrPaymentUpdateFailed
		}
		if locked == nil {
			return ErrPaymentNotFound
		}
		payment := *locked
		if payment.OrderID != 0 {
			output = &payment
			return nil
		}

		rechargeRepo := tx.Wallets().Wallets()
		recharge, err := rechargeRepo.GetRechargeOrderByPaymentIDForUpdate(payment.ID)
		if err != nil {
			return ErrPaymentUpdateFailed
		}
		if recharge == nil {
			return walletcontract.ErrRechargeNotFound
		}
		if !canExpireWalletRechargePayment(&payment, recharge) {
			output = &payment
			return nil
		}

		now := time.Now()
		if deferred, err := s.deferChainRechargeExpiry(tx, &payment, recharge, now); err != nil {
			return err
		} else if deferred {
			output = &payment
			return nil
		}
		payment.Status = constants.PaymentStatusExpired
		payment.ExpiredAt = &now
		payment.UpdatedAt = now
		if err := tx.Payments().Update(&payment); err != nil {
			return ErrPaymentUpdateFailed
		}

		recharge.Status = constants.WalletRechargeStatusExpired
		recharge.UpdatedAt = now
		if err := rechargeRepo.UpdateRechargeOrder(recharge); err != nil {
			return ErrPaymentUpdateFailed
		}
		output = &payment
		return nil
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

func canExpireWalletRechargePayment(payment *paymentdomain.Payment, recharge *walletdomain.RechargeOrder) bool {
	if payment == nil || recharge == nil || recharge.Status != constants.WalletRechargeStatusPending {
		return false
	}
	if payment.Status == constants.PaymentStatusSuccess || recharge.Status == constants.WalletRechargeStatusSuccess {
		return false
	}
	if payment.Status == constants.PaymentStatusFailed || recharge.Status == constants.WalletRechargeStatusFailed {
		return false
	}
	if payment.Status == constants.PaymentStatusExpired || recharge.Status == constants.WalletRechargeStatusExpired {
		return false
	}
	return payment.Status == constants.PaymentStatusInitiated || payment.Status == constants.PaymentStatusPending
}
