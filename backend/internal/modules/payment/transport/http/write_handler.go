package paymenthttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/i18n"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymentpresenter "github.com/dujiao-next/internal/modules/payment/transport/presenter"
	ginutil "github.com/dujiao-next/internal/platform/http/ginutil"
	"github.com/dujiao-next/internal/platform/http/response"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strconv"
	"strings"
)

var (
	ErrPaymentInvalid                      = errors.New("payment invalid")
	ErrPaymentNotFound                     = errors.New("payment not found")
	ErrOrderStatusInvalid                  = errors.New("order status invalid")
	ErrPaymentChannelNotFound              = errors.New("payment channel not found")
	ErrPaymentChannelInactive              = errors.New("payment channel inactive")
	ErrPaymentProviderNotSupported         = errors.New("payment provider not supported")
	ErrPaymentChannelConfigInvalid         = errors.New("payment channel config invalid")
	ErrPaymentGatewayRequestFailed         = errors.New("payment gateway request failed")
	ErrPaymentGatewayResponseInvalid       = errors.New("payment gateway response invalid")
	ErrPaymentCurrencyMismatch             = errors.New("payment currency mismatch")
	ErrWalletNotSupportedForGuest          = errors.New("wallet not supported for guest")
	ErrPaymentChannelNotAllowedForProduct  = errors.New("payment channel not allowed for product")
	ErrPaymentChannelNotAllowedForRecharge = errors.New("payment channel not allowed for wallet recharge")
	ErrWalletOnlyPaymentRequired           = errors.New("wallet only payment required")
	ErrChainInvoiceLimit                   = errors.New("该订单已达到链上付款地址生成次数上限，请使用已有付款单或联系客服")
	ErrPaymentStatusInvalid                = errors.New("payment status invalid")
	ErrPaymentAmountMismatch               = errors.New("payment amount mismatch")
)

// CreatePaymentInput 创建支付输入。
type CreatePaymentInput struct {
	OrderID       uint
	ChannelID     uint
	UseBalance    bool
	ClientIP      string
	Context       context.Context
	RequestScheme string
}

// CreatePaymentResult 创建支付结果。
type CreatePaymentResult struct {
	Payment          *paymentdomain.Payment
	Channel          *paymentdomain.PaymentChannel
	OrderPaid        bool
	WalletPaidAmount money.Amount
	OnlinePayAmount  money.Amount
}

// CapturePaymentInput 捕获支付输入。
type CapturePaymentInput struct {
	PaymentID uint
	Context   context.Context
}

// PaymentWriter 支付创建/查询/捕获端口。
type PaymentWriter interface {
	CreatePayment(input CreatePaymentInput) (*CreatePaymentResult, error)
	GetPayment(id uint) (*paymentdomain.Payment, error)
	CapturePayment(input CapturePaymentInput) (*paymentdomain.Payment, error)
}

// CreatePaymentRequest 用户创建支付请求。
type CreatePaymentRequest struct {
	OrderNo    string `json:"order_no" binding:"required"`
	ChannelID  uint   `json:"channel_id"`
	UseBalance bool   `json:"use_balance"`
}

// CreateGuestPaymentRequest 游客发起支付请求。
type CreateGuestPaymentRequest struct {
	OrderNo   string `json:"order_no" binding:"required"`
	ChannelID uint   `json:"channel_id" binding:"required"`
}

// WriteHandler 处理前台支付创建与捕获 HTTP。
type WriteHandler struct {
	guestOrders GuestOrderLookup
	userOrders  UserOrderLookup
	payments    PaymentWriter
}

func NewWriteHandler(guestOrders GuestOrderLookup, userOrders UserOrderLookup, payments PaymentWriter) *WriteHandler {
	if guestOrders == nil || userOrders == nil || payments == nil {
		panic("payment write handler: required dependency is nil")
	}
	return &WriteHandler{guestOrders: guestOrders, userOrders: userOrders, payments: payments}
}

// CreatePayment 创建支付单
func (h *WriteHandler) CreatePayment(c *gin.Context) {
	uid, ok := ginutil.GetUserID(c)
	if !ok {
		return
	}

	var req CreatePaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}

	order, err := h.userOrders.GetOrderByUserOrderNoForTenant(tenantFromRequest(c), req.OrderNo, uid)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.order_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}

	result, err := h.payments.CreatePayment(CreatePaymentInput{
		OrderID:       order.ID,
		ChannelID:     req.ChannelID,
		UseBalance:    req.UseBalance,
		ClientIP:      c.ClientIP(),
		Context:       c.Request.Context(),
		RequestScheme: requestSchemeFromContext(c),
	})
	if err != nil {
		respondPaymentCreateError(c, err)
		return
	}

	response.Success(c, paymentpresenter.NewCreatePaymentResp(&paymentpresenter.CreatePaymentResultView{
		Payment:          result.Payment,
		Channel:          result.Channel,
		OrderPaid:        result.OrderPaid,
		WalletPaidAmount: result.WalletPaidAmount,
		OnlinePayAmount:  result.OnlinePayAmount,
	}))
}

// CapturePayment 用户捕获支付。
func (h *WriteHandler) CapturePayment(c *gin.Context) {
	uid, ok := ginutil.GetUserID(c)
	if !ok {
		return
	}
	paymentID, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return
	}
	payment, err := h.payments.GetPayment(paymentID)
	if err != nil {
		if errors.Is(err, ErrPaymentNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	if _, err := h.userOrders.GetOrderByUserForTenant(tenantFromRequest(c), payment.OrderID, uid); err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.order_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}
	updated, err := h.payments.CapturePayment(CapturePaymentInput{
		PaymentID: paymentID,
		Context:   c.Request.Context(),
	})
	if err != nil {
		respondPaymentCaptureError(c, err)
		return
	}
	response.Success(c, gin.H{
		"payment_id": updated.ID,
		"status":     updated.Status,
	})
}

// CreateGuestPayment 游客发起支付
func (h *WriteHandler) CreateGuestPayment(c *gin.Context) {
	var req CreateGuestPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}
	email, password, ok := ginutil.GetGuestCredentials(c)
	if !ok || email == "" {
		ginutil.RespondError(c, response.CodeBadRequest, "error.guest_email_required", nil)
		return
	}
	if password == "" {
		ginutil.RespondError(c, response.CodeBadRequest, "error.guest_password_required", nil)
		return
	}
	guestOrder, err := h.guestOrders.GetOrderByGuestOrderNoForTenant(tenantFromRequest(c), req.OrderNo, email, password)
	if err != nil {
		if errors.Is(err, ErrGuestOrderNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.guest_order_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}
	result, err := h.payments.CreatePayment(CreatePaymentInput{
		OrderID:       guestOrder.ID,
		ChannelID:     req.ChannelID,
		UseBalance:    false,
		ClientIP:      c.ClientIP(),
		Context:       c.Request.Context(),
		RequestScheme: requestSchemeFromContext(c),
	})
	if err != nil {
		respondPaymentCreateError(c, err)
		return
	}
	response.Success(c, paymentpresenter.NewCreatePaymentResp(&paymentpresenter.CreatePaymentResultView{
		Payment:          result.Payment,
		Channel:          result.Channel,
		OrderPaid:        result.OrderPaid,
		WalletPaidAmount: result.WalletPaidAmount,
		OnlinePayAmount:  result.OnlinePayAmount,
	}))
}

// CaptureGuestPayment 游客捕获支付。
func (h *WriteHandler) CaptureGuestPayment(c *gin.Context) {
	paymentID, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return
	}
	email, password, ok := ginutil.GetGuestCredentials(c)
	if !ok || email == "" {
		ginutil.RespondError(c, response.CodeBadRequest, "error.guest_email_required", nil)
		return
	}
	if password == "" {
		ginutil.RespondError(c, response.CodeBadRequest, "error.guest_password_required", nil)
		return
	}

	payment, err := h.payments.GetPayment(paymentID)
	if err != nil {
		if errors.Is(err, ErrPaymentNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	if _, err := h.guestOrders.GetOrderByGuestForTenant(tenantFromRequest(c), payment.OrderID, email, password); err != nil {
		if errors.Is(err, ErrGuestOrderNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.guest_order_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}

	updated, err := h.payments.CapturePayment(CapturePaymentInput{
		PaymentID: paymentID,
		Context:   c.Request.Context(),
	})
	if err != nil {
		respondPaymentCaptureError(c, err)
		return
	}
	response.Success(c, gin.H{
		"payment_id": updated.ID,
		"status":     updated.Status,
	})
}

func requestSchemeFromContext(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); proto != "" {
		proto = strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
		if proto == "http" || proto == "https" {
			return proto
		}
	}
	if c.Request.TLS != nil {
		return "https"
	}
	return "http"
}

type mappedError struct {
	target error
	code   int
	key    string
	logErr bool
}

func respondWithMappedError(c *gin.Context, err error, rules []mappedError, fallbackCode int, fallbackKey string) {
	if retryAfter := retryAfterSeconds(err); retryAfter > 0 {
		c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
		locale := i18n.ResolveLocale(c)
		msg := fmt.Sprintf("%s (%ds)", i18n.T(locale, "error.risk_order_rate_limited"), retryAfter)
		response.Error(c, response.CodeTooManyRequests, msg)
		return
	}
	for _, rule := range rules {
		if errors.Is(err, rule.target) {
			var cause error
			if rule.logErr {
				cause = err
			}
			ginutil.RespondError(c, rule.code, rule.key, cause)
			return
		}
	}
	ginutil.RespondError(c, fallbackCode, fallbackKey, err)
}

type retryAfterCarrier interface {
	RetryAfterSeconds() int64
}

func retryAfterSeconds(err error) int64 {
	var carrier retryAfterCarrier
	if errors.As(err, &carrier) {
		return carrier.RetryAfterSeconds()
	}
	return 0
}

func respondPaymentCreateError(c *gin.Context, err error) {
	if errors.Is(err, ErrChainInvoiceLimit) {
		response.ErrorWithHTTPStatus(c, 429, response.CodeTooManyRequests, ErrChainInvoiceLimit.Error())
		return
	}
	if errors.Is(err, contract.ErrChainAmountExhausted) {
		ginutil.RespondError(c, response.CodeBadRequest, "error.chain_amount_slots_exhausted", nil)
		return
	}
	respondWithMappedError(c, err, paymentCreateErrorRules, response.CodeInternal, "error.payment_create_failed")
}

func respondPaymentCaptureError(c *gin.Context, err error) {
	respondWithMappedError(c, err, paymentCaptureErrorRules, response.CodeInternal, "error.payment_callback_failed")
}

var paymentProviderGatewayErrorRules = []mappedError{
	{target: ErrPaymentProviderNotSupported, code: response.CodeBadRequest, key: "error.payment_provider_not_supported"},
	{target: ErrPaymentChannelConfigInvalid, code: response.CodeBadRequest, key: "error.payment_channel_config_invalid"},
	{target: ErrPaymentGatewayRequestFailed, code: response.CodeBadRequest, key: "error.payment_gateway_request_failed"},
	{target: ErrPaymentGatewayResponseInvalid, code: response.CodeBadRequest, key: "error.payment_gateway_response_invalid"},
}

var paymentCreateErrorRules = concatMappedErrors(
	[]mappedError{
		{target: ErrPaymentInvalid, code: response.CodeBadRequest, key: "error.payment_invalid"},
		{target: ErrOrderNotFound, code: response.CodeNotFound, key: "error.order_not_found"},
		{target: ErrOrderStatusInvalid, code: response.CodeBadRequest, key: "error.order_status_invalid"},
		{target: ErrPaymentChannelNotFound, code: response.CodeNotFound, key: "error.payment_channel_not_found"},
		{target: ErrPaymentChannelInactive, code: response.CodeBadRequest, key: "error.payment_channel_inactive"},
	},
	paymentProviderGatewayErrorRules,
	[]mappedError{
		{target: ErrPaymentCurrencyMismatch, code: response.CodeBadRequest, key: "error.payment_currency_mismatch"},
		{target: ErrWalletNotSupportedForGuest, code: response.CodeBadRequest, key: "error.payment_invalid"},
		{target: ErrPaymentChannelNotAllowedForProduct, code: response.CodeBadRequest, key: "error.payment_channel_not_allowed_for_product"},
		{target: ErrPaymentChannelNotAllowedForRecharge, code: response.CodeBadRequest, key: "error.payment_channel_not_allowed_for_recharge"},
		{target: ErrWalletOnlyPaymentRequired, code: response.CodeBadRequest, key: "error.wallet_only_payment_required"},
	},
)

var paymentCaptureErrorRules = concatMappedErrors(
	[]mappedError{
		{target: ErrPaymentInvalid, code: response.CodeBadRequest, key: "error.payment_invalid"},
		{target: ErrPaymentNotFound, code: response.CodeNotFound, key: "error.payment_not_found"},
		{target: ErrPaymentChannelNotFound, code: response.CodeNotFound, key: "error.payment_channel_not_found"},
	},
	paymentProviderGatewayErrorRules,
	[]mappedError{
		{target: ErrPaymentStatusInvalid, code: response.CodeBadRequest, key: "error.payment_status_invalid"},
		{target: ErrPaymentAmountMismatch, code: response.CodeBadRequest, key: "error.payment_amount_mismatch"},
		{target: ErrPaymentCurrencyMismatch, code: response.CodeBadRequest, key: "error.payment_currency_mismatch"},
		{target: ErrOrderNotFound, code: response.CodeNotFound, key: "error.order_not_found"},
	},
)

func concatMappedErrors(groups ...[]mappedError) []mappedError {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	result := make([]mappedError, 0, total)
	for _, group := range groups {
		result = append(result, group...)
	}
	return result
}

// ReadSubmittedHash rejects extra fields, multiple JSON values and oversized
// bodies. An empty body/hash asks only to recheck a previously submitted hash.
func ReadSubmittedHash(c *gin.Context) (string, bool) {
	var in struct {
		Hash string `json:"transaction_hash"`
	}
	d := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 2048))
	d.DisallowUnknownFields()
	e := d.Decode(&in)
	if e != nil && e != io.EOF {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return "", false
	}
	if e == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
			return "", false
		}
	}
	return in.Hash, true
}
func (h *WriteHandler) SubmitTransaction(c *gin.Context) {
	uid, ok := ginutil.GetUserID(c)
	if !ok {
		return
	}
	p, ok := h.submittedPayment(c)
	if !ok {
		return
	}
	if _, e := h.userOrders.GetOrderByUserForTenant(tenantFromRequest(c), p.OrderID, uid); e != nil {
		ginutil.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
		return
	}
	h.submitOwned(c, p, uid)
}
func (h *WriteHandler) SubmitGuestTransaction(c *gin.Context) {
	email, password, ok := ginutil.GetGuestCredentials(c)
	if !ok {
		ginutil.RespondError(c, response.CodeUnauthorized, "error.guest_order_not_found", nil)
		return
	}
	p, ok := h.submittedPayment(c)
	if !ok {
		return
	}
	if _, e := h.guestOrders.GetOrderByGuestForTenant(tenantFromRequest(c), p.OrderID, email, password); e != nil {
		ginutil.RespondError(c, response.CodeNotFound, "error.guest_order_not_found", nil)
		return
	}
	h.submitOwned(c, p, 0)
}
func (h *WriteHandler) submittedPayment(c *gin.Context) (*paymentdomain.Payment, bool) {
	id, e := ginutil.ParseParamUint(c, "id")
	if e != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return nil, false
	}
	p, e := h.payments.GetPayment(id)
	if e != nil || p == nil || p.ProviderType != "selfhost" || p.OrderID == 0 {
		ginutil.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
		return nil, false
	}
	return p, true
}
func (h *WriteHandler) submitOwned(c *gin.Context, p *paymentdomain.Payment, uid uint) {
	hash, ok := ReadSubmittedHash(c)
	if !ok {
		return
	}
	service, ok := h.payments.(contract.TransactionSubmitter)
	if !ok {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", nil)
		return
	}
	updated, e := service.SubmitTransaction(contract.SubmitTransactionInput{PaymentID: p.ID, TransactionHash: hash, AuthorizedOrderID: p.OrderID, AuthorizedUserID: uid, Context: c.Request.Context()})
	if e != nil {
		respondPaymentCaptureError(c, e)
		return
	}
	response.Success(c, paymentpresenter.NewSubmittedTransactionResp(updated))
}
