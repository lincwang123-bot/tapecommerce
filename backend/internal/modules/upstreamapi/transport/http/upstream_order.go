package upstreamhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/voucherbridge"
	"github.com/shopspring/decimal"
	"net/http"
	"os"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	downstreamcallbackdomain "github.com/dujiao-next/internal/modules/downstreamcallback/domain"
	"github.com/dujiao-next/internal/platform/http/ginutil"
	"github.com/dujiao-next/internal/shared/jsonmap"

	"github.com/gin-gonic/gin"
)

type createOrderRequest struct {
	MaxTotal          string       `json:"max_total"`
	ClientOrderID     string       `json:"client_order_id"`
	SKUID             uint         `json:"sku_id" binding:"required"`
	Quantity          int          `json:"quantity" binding:"required,min=1"`
	ManualFormData    jsonmap.JSON `json:"manual_form_data"`
	DownstreamOrderNo string       `json:"downstream_order_no"`
	TraceID           string       `json:"trace_id"`
	CallbackURL       string       `json:"callback_url"`
}

// CreateOrder POST /api/v1/upstream/orders
func (h *Handler) CreateOrder(c *gin.Context) {
	userID := getUpstreamUserID(c)
	credentialID := getUpstreamCredentialID(c)
	if userID == 0 || credentialID == 0 {
		errorResponse(c, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}

	var req createOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		errorResponse(c, http.StatusBadRequest, "bad_request", "invalid request body: "+err.Error())
		return
	}

	// 验证 callback URL（防止 SSRF）
	if req.CallbackURL != "" {
		if err := validateCallbackURL(req.CallbackURL); err != nil {
			errorResponse(c, http.StatusBadRequest, "invalid_callback_url", err.Error())
			return
		}
	}

	if req.DownstreamOrderNo == "" {
		req.DownstreamOrderNo = req.ClientOrderID
	}
	if len(req.DownstreamOrderNo) < 4 || len(req.DownstreamOrderNo) > 64 || req.Quantity > 100 {
		errorResponse(c, 400, "invalid_request", "client_order_id/downstream_order_no required (4-64 chars), max quantity 100")
		return
	}

	maxTotal, maxErr := decimal.NewFromString(req.MaxTotal)
	if voucherbridge.Enabled() && (maxErr != nil || !maxTotal.IsPositive()) {
		errorResponse(c, 400, "max_total_required", "positive max_total USDT required")
		return
	}
	// 查找 SKU 获取所属商品 ID
	sku, err := h.SKUs.GetByID(req.SKUID)
	if err != nil || sku == nil {
		errorResponse(c, http.StatusBadRequest, "sku_unavailable", "sku not found")
		return
	}
	if !sku.IsActive {
		errorResponse(c, http.StatusBadRequest, "sku_unavailable", "sku is not active")
		return
	}

	capText := os.Getenv("VOUCHER_API_MAX_ORDER_USDT")
	if capText == "" {
		capText = "1000"
	}
	capAmount, capErr := decimal.NewFromString(capText)
	if capErr != nil || !capAmount.IsPositive() {
		errorResponse(c, 403, "spend_limit", "API order spend limit exceeded")
		return
	}
	// 验证商品是否上架
	product, err := h.ProductRepository.GetByID(fmt.Sprintf("%d", sku.ProductID))
	if err != nil || product == nil || !product.IsActive {
		errorResponse(c, http.StatusBadRequest, "product_unavailable", "product is not available")
		return
	}

	// 构建手动表单数据
	var manualFormData map[string]jsonmap.JSON
	if req.ManualFormData != nil && product.FulfillmentType == constants.FulfillmentTypeManual {
		manualFormData = map[string]jsonmap.JSON{
			fmt.Sprintf("%d", sku.ProductID): req.ManualFormData,
		}
	}

	// 创建订单（下游订单跳过风控：已通过 API 签名认证，自动钱包扣款，不存在占库存问题）
	metadata, _ := json.Marshal([]string{req.CallbackURL, req.MaxTotal})
	input := CreateOrderInput{
		ClientRequestID: fmt.Sprintf("api:%d:%s", credentialID, req.DownstreamOrderNo),
		RequestMetadata: string(metadata),
		UserID:          userID,
		Items: []CreateOrderItem{
			{
				ProductID:       sku.ProductID,
				SKUID:           req.SKUID,
				Quantity:        req.Quantity,
				FulfillmentType: product.FulfillmentType,
			},
		},
		ClientIP:        c.ClientIP(),
		ManualFormData:  manualFormData,
		SkipRiskControl: true,
	}

	order, err := h.Orders.CreateOrder(input)
	if err != nil {
		mapOrderErrorToResponse(c, err)
		return
	}

	if voucherbridge.Enabled() && (order.Currency != "USDT" || order.TotalAmount.Decimal.GreaterThan(maxTotal)) {
		errorResponse(c, 409, "quote_changed", "order exceeds max_total")
		return
	}
	if order.TotalAmount.Decimal.GreaterThan(capAmount) {
		errorResponse(c, 403, "spend_limit", "API order spend limit exceeded")
		return
	}
	// 创建下游订单引用记录（用于回调通知下游）
	ref := &downstreamcallbackdomain.OrderRef{
		OrderID:           order.ID,
		ApiCredentialID:   credentialID,
		DownstreamOrderNo: req.DownstreamOrderNo,
		CallbackURL:       req.CallbackURL,
		TraceID:           req.TraceID,
		CallbackStatus:    downstreamcallbackdomain.StatusPending,
	}
	if createErr := h.DownstreamRefs.Create(ref); createErr != nil {
		existing, lookupErr := h.DownstreamRefs.GetByCredentialAndDownstreamNo(credentialID, req.DownstreamOrderNo)
		if lookupErr != nil || existing == nil || existing.OrderID != order.ID || existing.CallbackURL != req.CallbackURL {
			errorResponse(c, 503, "callback_reference_unavailable", "retry the same client_order_id")
			return
		}
		logger.Errorw("upstream_create_downstream_ref_failed",
			"order_id", order.ID,
			"credential_id", credentialID,
			"downstream_order_no", req.DownstreamOrderNo,
			"callback_url", req.CallbackURL,
			"trace_id", req.TraceID,
			"error", createErr,
		)
	} else {
		logger.Infow("upstream_downstream_ref_created",
			"ref_id", ref.ID,
			"order_id", order.ID,
			"callback_url", req.CallbackURL,
		)
	}

	if order.PaidAt != nil {
		successResponse(c, gin.H{"ok": true, "order_id": order.ID, "order_no": order.OrderNo, "status": order.Status, "amount": order.TotalAmount.StringFixed(2), "currency": order.Currency})
		return
	}
	// 自动使用钱包余额支付（上游 API 订单默认钱包扣款）
	payResult, payErr := h.Payments.CreatePayment(CreatePaymentInput{
		OrderID:    order.ID,
		UseBalance: true,
		ClientIP:   c.ClientIP(),
	})
	if payErr != nil {
		logger.Errorw("upstream_auto_wallet_pay_failed",
			"order_id", order.ID,
			"error", payErr,
		)
		// 支付失败，自动取消订单避免遗留未支付订单
		if _, cancelErr := h.Orders.CancelOrder(order.ID, userID); cancelErr != nil {
			logger.Warnw("upstream_cancel_unpaid_order_failed", "order_id", order.ID, "error", cancelErr)
		}
		// 钱包余额不足或支付失败，返回 200 + ok:false 让 A 站正确解析错误码
		c.JSON(http.StatusOK, gin.H{
			"ok":            false,
			"order_id":      order.ID,
			"order_no":      order.OrderNo,
			"status":        constants.OrderStatusCanceled,
			"error_code":    "payment_failed",
			"error_message": fmt.Sprintf("wallet payment failed: %s", payErr.Error()),
		})
		return
	}

	// 刷新订单状态
	finalStatus := order.Status
	if payResult != nil && payResult.OrderPaid {
		finalStatus = constants.OrderStatusPaid
	}

	// 币种
	currency, _ := h.Settings.GetSiteCurrency("CNY")

	successResponse(c, gin.H{
		"ok":       true,
		"order_id": order.ID,
		"order_no": order.OrderNo,
		"status":   finalStatus,
		"amount":   order.TotalAmount.StringFixed(2),
		"currency": currency,
	})
}

// GetOrder GET /api/v1/upstream/orders/:id
func (h *Handler) GetOrder(c *gin.Context) {
	userID := getUpstreamUserID(c)
	if userID == 0 {
		errorResponse(c, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}

	orderID, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		errorResponse(c, http.StatusBadRequest, "bad_request", "invalid order id")
		return
	}

	order, err := h.Orders.GetOrderByUser(orderID, userID)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			errorResponse(c, http.StatusNotFound, "order_not_found", "order not found")
			return
		}
		logger.Errorw("upstream_get_order_failed", "order_id", orderID, "error", err)
		errorResponse(c, http.StatusInternalServerError, "internal_error", "failed to get order")
		return
	}

	status := strings.ToLower(strings.TrimSpace(order.Status))
	localRefundRecords := make([]jsonmap.JSON, 0)

	// 优先使用采购单视角的上游退款信息，避免订单状态与上游退款状态不一致。
	if h.Procurements != nil {
		procOrder, procErr := h.Procurements.GetByLocalOrderNo(order.OrderNo)
		if procErr == nil && procOrder != nil {
			switch strings.ToLower(strings.TrimSpace(procOrder.Status)) {
			case constants.ProcurementStatusPartiallyRefunded, constants.ProcurementStatusRefunded:
				status = strings.ToLower(strings.TrimSpace(procOrder.Status))
			}
		}
	}

	// refund_records 固定返回本地退款记录（与更上游无关），由服务层统一处理。
	if records, recordsErr := h.Orders.BuildLocalRefundRecordsForOrder(order); recordsErr != nil {
		logger.Warnw("upstream_get_order_refund_records_failed", "order_id", order.ID, "error", recordsErr)
	} else {
		localRefundRecords = records
	}

	resp := gin.H{
		"ok":              true,
		"order_id":        order.ID,
		"order_no":        order.OrderNo,
		"status":          status,
		"amount":          order.TotalAmount.StringFixed(2),
		"refunded_amount": order.RefundedAmount.StringFixed(2),
		"currency":        order.Currency,
		"refund_records":  localRefundRecords,
	}

	// 若已交付，返回交付信息（优先使用订单自身的 fulfillment，否则从子订单获取）
	sourceFulfillment := order.Fulfillment
	if sourceFulfillment == nil && len(order.Children) > 0 {
		for i := range order.Children {
			if order.Children[i].Fulfillment != nil {
				sourceFulfillment = order.Children[i].Fulfillment
				break
			}
		}
	}
	if sourceFulfillment != nil && sourceFulfillment.Status == constants.FulfillmentStatusDelivered {
		resp["fulfillment"] = gin.H{
			"type":          sourceFulfillment.Type,
			"status":        sourceFulfillment.Status,
			"payload":       sourceFulfillment.Payload,
			"delivery_data": sourceFulfillment.LogisticsJSON,
			"delivered_at":  sourceFulfillment.DeliveredAt,
		}
	}

	// 订单项信息
	if len(order.Items) > 0 {
		items := make([]gin.H, 0, len(order.Items))
		for _, item := range order.Items {
			items = append(items, gin.H{
				"product_id":           item.ProductID,
				"sku_id":               item.SKUID,
				"title":                item.TitleJSON,
				"quantity":             item.Quantity,
				"original_unit_price":  item.OriginalUnitPrice.StringFixed(2),
				"unit_price":           item.UnitPrice.StringFixed(2),
				"original_total_price": item.OriginalTotalPrice.StringFixed(2),
				"total_price":          item.TotalPrice.StringFixed(2),
				"fulfillment_type":     item.FulfillmentType,
			})
		}
		resp["items"] = items
	}

	successResponse(c, resp)
}

// CancelOrder POST /api/v1/upstream/orders/:id/cancel
func (h *Handler) CancelOrder(c *gin.Context) {
	userID := getUpstreamUserID(c)
	if userID == 0 {
		errorResponse(c, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}

	orderID, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		errorResponse(c, http.StatusBadRequest, "bad_request", "invalid order id")
		return
	}

	order, err := h.Orders.CancelOrder(orderID, userID)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			errorResponse(c, http.StatusNotFound, "order_not_found", "order not found")
			return
		}
		if errors.Is(err, ErrOrderCancelNotAllowed) {
			errorResponse(c, http.StatusConflict, "cancel_not_allowed", "order cannot be canceled in current status")
			return
		}
		logger.Errorw("upstream_cancel_order_failed", "order_id", orderID, "error", err)
		errorResponse(c, http.StatusInternalServerError, "internal_error", "failed to cancel order")
		return
	}

	successResponse(c, gin.H{
		"ok":       true,
		"order_id": order.ID,
		"order_no": order.OrderNo,
		"status":   order.Status,
	})
}

func mapOrderErrorToResponse(c *gin.Context, err error) {
	switch {
	case err != nil && err.Error() == "idempotency_conflict":
		errorResponse(c, http.StatusConflict, "idempotency_conflict", "client_order_id was already used with different parameters")
	case errors.Is(err, ErrWalletInsufficient):
		errorResponse(c, http.StatusPaymentRequired, "insufficient_balance", "wallet balance is insufficient")
	case errors.Is(err, ErrStockInsufficient):
		errorResponse(c, http.StatusConflict, "insufficient_stock", "product stock is insufficient")
	case errors.Is(err, ErrProductUnavailable):
		errorResponse(c, http.StatusBadRequest, "product_unavailable", "product is not available")
	case errors.Is(err, ErrSKUUnavailable):
		errorResponse(c, http.StatusBadRequest, "sku_unavailable", "sku is invalid or not available")
	case errors.Is(err, ErrInvalidOrderItem):
		errorResponse(c, http.StatusBadRequest, "bad_request", "invalid order parameters")
	case errors.Is(err, ErrManualFormInvalid):
		errorResponse(c, http.StatusBadRequest, "bad_request", "manual form data is invalid: "+err.Error())
	default:
		logger.Errorw("upstream_create_order_failed", "error", err)
		errorResponse(c, http.StatusInternalServerError, "internal_error", "failed to create order")
	}
}
