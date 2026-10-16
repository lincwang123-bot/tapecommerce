package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	chain "github.com/dujiao-next/internal/modules/payment/application/chainpayment"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"strings"
	"time"
)

// CapturePaymentInput 捕获支付输入。
type CapturePaymentInput struct {
	PaymentID uint
	Context   context.Context
}

func (s *PaymentService) CapturePayment(input CapturePaymentInput) (*paymentdomain.Payment, error) {
	if input.PaymentID == 0 {
		return nil, ErrPaymentInvalid
	}
	payment, err := s.paymentRepo.GetByID(input.PaymentID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if payment == nil {
		return nil, ErrPaymentNotFound
	}
	if payment.Status == constants.PaymentStatusSuccess {
		return payment, nil
	}

	channel, err := s.channelRepo.GetByID(payment.ChannelID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if channel == nil {
		return nil, ErrPaymentChannelNotFound
	}

	providerType := strings.ToLower(strings.TrimSpace(channel.ProviderType))
	isNowPayments := providerType == "nowpayments" && strings.EqualFold(strings.TrimSpace(channel.ChannelType), "usdttrc20")
	if providerType != constants.PaymentProviderOfficial && !isNowPayments {
		return nil, ErrPaymentProviderNotSupported
	}
	if isNowPayments && (!strings.EqualFold(payment.ProviderType, "nowpayments") || !strings.EqualFold(payment.ChannelType, "usdttrc20")) {
		return nil, ErrPaymentGatewayResponseInvalid
	}
	if strings.TrimSpace(payment.ProviderRef) == "" {
		return nil, ErrPaymentInvalid
	}

	// 统一通过 Registry 路由。Registry.Lookup 会返回 channel 对应的 adapter,
	// 如果 adapter 不实现 Capturer,type assertion 失败,返回 ErrPaymentProviderNotSupported。
	// 因此无需在此显式检查 channel 是否支持 capture。
	return s.captureViaRegistry(input, payment, channel)
}

// captureViaRegistry 通过 PaymentProviderRegistry 路由调用 QueryPayment。
// stripe + paypal + wechat + nowpayments 实现了 paymentcontract.GatewayCapturer 接口,其它 channel
// (alipay / epay / epusdt / bepusdt / tokenpay / okpay) 仅实现 webhook 回调,
// type assertion 失败时返回 ErrPaymentProviderNotSupported。
func (s *PaymentService) captureViaRegistry(input CapturePaymentInput, payment *paymentdomain.Payment, channel *paymentdomain.PaymentChannel) (*paymentdomain.Payment, error) {
	logger.Infow("payment_capture_via_registry",
		"payment_id", payment.ID,
		"provider_type", channel.ProviderType,
		"channel_type", channel.ChannelType,
	)
	if s.paymentProviderRegistry == nil {
		return nil, ErrPaymentProviderNotSupported
	}
	p, ok := s.paymentProviderRegistry.Lookup(channel.ProviderType, channel.ChannelType)
	if !ok {
		return nil, ErrPaymentProviderNotSupported
	}
	capturer, ok := p.(paymentcontract.GatewayCapturer)
	if !ok {
		logger.Warnw("payment_provider_capture_not_implemented",
			"provider_type", channel.ProviderType,
			"channel_type", channel.ChannelType,
		)
		return nil, ErrPaymentProviderNotSupported
	}

	// 第二参数是 interactionMode,不是 channelType。stripe/wechat adapter
	// 会拒绝任何非法 mode,传 channelType 会导致  一律 ErrConfigInvalid。
	if err := capturer.ValidateConfig(channel.ConfigJSON, channel.InteractionMode); err != nil {
		return nil, mapProviderErrorToService(err)
	}

	ctx, cancel := detachOutboundRequestContext(input.Context)
	defer cancel()

	queryResult, err := capturer.QueryPayment(ctx, channel.ConfigJSON, payment.ProviderRef)
	if err != nil {
		return nil, mapProviderErrorToService(err)
	}
	if queryResult == nil {
		return nil, ErrPaymentGatewayResponseInvalid
	}

	payload := jsonmap.JSON{}
	if queryResult.Payload != nil {
		payload = queryResult.Payload
	}
	orderNo := ""
	if strings.EqualFold(strings.TrimSpace(channel.ProviderType), "nowpayments") {
		// QueryPayment 校验币种、实付及 finished 状态；这里将查单结果绑定到
		// 本地支付单，不能仅凭 provider ref 将另一笔订单的款记入本单。
		orderNo, _ = payload["order_id"].(string)
		if orderNo == "" || payment.GatewayOrderNo == "" || orderNo != payment.GatewayOrderNo || queryResult.ProviderRef != payment.ProviderRef {
			return nil, ErrPaymentGatewayResponseInvalid
		}
	}
	status := strings.TrimSpace(queryResult.Status)
	if status == "" {
		status = constants.PaymentStatusPending
	}

	callbackInput := PaymentCallbackInput{
		PaymentID:   payment.ID,
		OrderNo:     orderNo,
		ChannelID:   channel.ID,
		Status:      status,
		ProviderRef: pickFirstNonEmpty(queryResult.ProviderRef, payment.ProviderRef),
		Amount:      queryResult.Amount,
		Currency:    strings.ToUpper(strings.TrimSpace(queryResult.Currency)),
		PaidAt:      queryResult.PaidAt,
		Payload:     payload,
	}
	return s.HandleCallback(callbackInput)
}

// mapProviderErrorToService 把 provider.ErrXxx 转换为 service 层的 ErrPaymentXxx。
func mapProviderErrorToService(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, paymentcontract.ErrGatewayConfigInvalid):
		return fmt.Errorf("%w: %v", ErrPaymentChannelConfigInvalid, err)
	case errors.Is(err, paymentcontract.ErrGatewayRequestFailed), errors.Is(err, paymentcontract.ErrGatewayAuthFailed):
		return fmt.Errorf("%w: %v", ErrPaymentGatewayRequestFailed, err)
	case errors.Is(err, paymentcontract.ErrGatewayResponseInvalid), errors.Is(err, paymentcontract.ErrGatewaySignatureInvalid):
		return fmt.Errorf("%w: %v", ErrPaymentGatewayResponseInvalid, err)
	case errors.Is(err, paymentcontract.ErrGatewayUnsupportedChannel), errors.Is(err, paymentcontract.ErrGatewayProviderNotFound):
		return ErrPaymentProviderNotSupported
	default:
		return fmt.Errorf("%w: %v", ErrPaymentGatewayRequestFailed, err)
	}
}

type SubmitTransactionInput struct {
	PaymentID                           uint
	TransactionHash                     string
	AuthorizedOrderID, AuthorizedUserID uint
	Context                             context.Context
}
type verifiedChainPayment struct {
	intent   *paymentdomain.ChainIntent
	evidence *paymentcontract.ChainEvidence
}

func (s *PaymentService) chainService() *chain.Service {
	return &chain.Service{Payments: s.paymentRepo, Verifier: s.chainVerifier, Settle: func(ctx context.Context, p *paymentdomain.Payment, v *paymentdomain.ChainIntent, e *paymentcontract.ChainEvidence) error {
		_, err := s.HandleCallback(PaymentCallbackInput{PaymentID: p.ID, ChannelID: p.ChannelID, Status: constants.PaymentStatusSuccess, ProviderRef: fmt.Sprintf("%s:%s:%d", v.Network, e.TxID, e.LogIndex), Currency: p.Currency, Amount: p.Amount, PaidAt: &e.PaidAt, verifiedChain: &verifiedChainPayment{v, e}})
		return err
	}}
}
func (s *PaymentService) SubmitTransaction(in SubmitTransactionInput) (*paymentdomain.Payment, error) {
	p, e := s.GetPayment(in.PaymentID)
	if e != nil {
		return nil, e
	}
	if p.ProviderType != "selfhost" {
		return nil, ErrPaymentProviderNotSupported
	}
	if p.OrderID > 0 {
		if in.AuthorizedOrderID != p.OrderID {
			return nil, ErrPaymentNotFound
		}
	} else {
		r, e := s.walletRepo.GetRechargeOrderByPaymentID(p.ID)
		if e != nil || r == nil || in.AuthorizedUserID == 0 || r.UserID != in.AuthorizedUserID {
			return nil, ErrPaymentNotFound
		}
	}
	p, e = s.chainService().Submit(in.Context, p.ID, in.TransactionHash)
	return p, mapChainError(e)
}
func mapChainError(e error) error {
	switch {
	case errors.Is(e, chain.ErrInvalid):
		return ErrPaymentInvalid
	case errors.Is(e, chain.ErrUnavailable):
		return ErrPaymentGatewayRequestFailed
	case errors.Is(e, chain.ErrConflict):
		return ErrPaymentStatusInvalid
	case errors.Is(e, chain.ErrExhausted):
		return paymentcontract.ErrChainAmountExhausted
	}
	return e
}
func (s *PaymentService) prepareChain(ctx context.Context, channelID uint) (*chain.Prepared, error) {
	if channelID == 0 {
		return nil, nil
	}
	c, e := s.channelRepo.GetByID(channelID)
	if e != nil || c == nil || c.ProviderType != "selfhost" {
		return nil, e
	}
	prepared, e := s.chainService().Prepare(ctx, c)
	return prepared, mapChainError(e)
}
func (s *PaymentService) reserveChain(tx paymentcontract.Transaction, p *paymentdomain.Payment, c *paymentdomain.PaymentChannel, userID uint, expires time.Time, prepared *chain.Prepared) error {
	if c.ProviderType != "selfhost" {
		return nil
	}
	// Expiry settings must be read before the transaction: its connection is
	// already held here, so a settings lookup can deadlock a single-connection pool.
	return mapChainError(s.chainService().Reserve(tx, p, c, userID, expires, prepared))
}
func (s *PaymentService) applyChainReceipt(tx paymentcontract.Transaction, p *paymentdomain.Payment, in PaymentCallbackInput, open bool) (bool, error) {
	if in.verifiedChain == nil || in.Status != constants.PaymentStatusSuccess {
		return false, ErrPaymentInvalid
	}
	ok, e := chain.Claim(tx, p, in.verifiedChain.intent, in.verifiedChain.evidence, open)
	if e != nil {
		return false, fmt.Errorf("%w: %w", mapChainError(e), e)
	}
	if !ok {
		p.ExceptionCode = "chain_review_required"
		p.ProviderRef = in.ProviderRef
		p.CallbackAt = &in.verifiedChain.evidence.PaidAt
	}
	return ok, nil
}
func chainOrderOpen(o *orderdomain.Order, p *paymentdomain.Payment, evidence *paymentcontract.ChainEvidence, now time.Time) bool {
	if o != nil {
		if o.RefundedAmount.Decimal.IsPositive() {
			return false
		}
		for _, child := range o.Children {
			if child.Status != constants.OrderStatusPendingPayment || child.PaidAt != nil || child.CanceledAt != nil || child.RefundedAmount.Decimal.IsPositive() {
				return false
			}
		}
	}
	return o != nil && p != nil && evidence != nil && o.Status == constants.OrderStatusPendingPayment && o.PaidAt == nil && o.CanceledAt == nil &&
		(o.ExpiresAt == nil || (!evidence.PaidAt.After(*o.ExpiresAt) && now.Before(o.ExpiresAt.Add(paymentdomain.ChainConfirmationGrace)))) &&
		!p.Amount.Decimal.LessThan(o.TotalAmount.Decimal.Sub(o.WalletPaidAmount.Decimal))
}

// Recovery rechecks submitted hashes only; it never discovers transfers or
// initiates procurement. Existing voucherbridge.Recovery repairs paid delivery.
func (s *PaymentService) RunChainChecks(ctx context.Context) error {
	if s.chainVerifier == nil {
		return nil
	}
	return s.chainService().RunOnce(ctx)
}

func (s *PaymentService) isStoreCryptoChannel(c *paymentdomain.PaymentChannel) bool {
	if c == nil {
		return false
	}
	if c.ProviderType == "nowpayments" && c.ChannelType == "usdttrc20" {
		return true
	}
	if c.ProviderType != "selfhost" || s.chainVerifier == nil {
		return false
	}
	_, ok := s.chainVerifier.Network(c.ChannelType)
	return ok
}

func (s *PaymentService) reusableChainPayment(tx paymentcontract.Transaction, orderID uint, c *paymentdomain.PaymentChannel) (*paymentdomain.Payment, error) {
	store := chain.Stores(tx)
	if store == nil {
		return nil, nil
	}
	rows, e := tx.Payments().ListByOrderID(orderID)
	if e != nil {
		return nil, e
	}
	var matched *paymentdomain.Payment
	for i := range rows {
		p := &rows[i]
		if p.ProviderType != "selfhost" {
			continue
		}
		v, e := store.Get(p.ID, false)
		if e != nil {
			return nil, e
		}
		if v == nil {
			continue
		}
		if v.SubmittedHash != "" && v.VerificationStatus != "rejected" && v.VerificationStatus != "verified" {
			return p, nil
		}
		if c != nil && c.ProviderType == "selfhost" && s.chainVerifier != nil && time.Now().Before(v.ExpiresAt) && p.Status == "pending" {
			n, ok := s.chainVerifier.Network(c.ChannelType)
			a, _ := c.ConfigJSON["receiving_address"].(string)
			normalized, valid := s.chainVerifier.NormalizeAddress(n.Network, a)
			if ok && valid && n.Network == v.Network && normalized == v.Recipient && (matched == nil || p.ID > matched.ID) {
				matched = p
			}
		}
	}
	return matched, nil
}

func (s *PaymentService) RunChainDiscovery(ctx context.Context, network string) (chain.DiscoveryReport, error) {
	return s.chainService().DiscoverOnce(ctx, network)
}
