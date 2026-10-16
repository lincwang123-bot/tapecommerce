package tapecommercebootstrap

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	reseller "github.com/dujiao-next/internal/modules/reseller/contract"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"github.com/dujiao-next/internal/platform/http/ginutil"
	"github.com/dujiao-next/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

func RegisterPublicRoutes(r gin.IRoutes, l *Live) {
	r.GET("/tapecommerce/config", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		tenant, ok := reseller.TenantFromContext(c.Request.Context())
		if ok && (!tenant.IsMain || tenant.Unavailable) {
			response.Success(c, PublicConfig{Mode: "live", ChainID: 196, Symbol: "OKB", TTL: 60, Reason: "tapecommerce_tenant_ineligible"})
			return
		}
		response.Success(c, l.PublicConfig(c.Request.Context()))
	})
}
func RegisterUserRoutes(r gin.IRoutes, l *Live) { registerLiveRoutes(r, l, false) }
func RegisterGuestRoutes(read, write gin.IRoutes, l *Live) {
	read.GET("/orders/:order_no/tapecommerce/status", l.statusHTTP(true))
	write.POST("/orders/:order_no/tapecommerce/quote", l.quoteHTTP(true))
	write.POST("/orders/:order_no/tapecommerce/submit", l.submitHTTP(true))
}
func registerLiveRoutes(r gin.IRoutes, l *Live, guest bool) {
	r.GET("/orders/:order_no/tapecommerce/status", l.statusHTTP(guest))
	r.POST("/orders/:order_no/tapecommerce/quote", l.quoteHTTP(guest))
	r.POST("/orders/:order_no/tapecommerce/submit", l.submitHTTP(guest))
}
func (l *Live) ownerHTTP(c *gin.Context, guest bool) (Owner, error) {
	if l == nil || l.Orders == nil {
		return Owner{}, domain.ErrNotFound
	}
	tenant, ok := reseller.TenantFromContext(c.Request.Context())
	if !ok || tenant.Unavailable || !tenant.IsMain || tenant.ResellerID != nil {
		return Owner{}, domain.ErrNotFound
	}
	no := strings.TrimSpace(c.Param("order_no"))
	if no == "" || len(no) > 128 {
		return Owner{}, domain.ErrNotFound
	}
	if guest {
		email, password, ok := ginutil.GetGuestCredentials(c)
		if !ok || email == "" || password == "" {
			return Owner{}, domain.ErrNotFound
		}
		o, e := l.Orders.GetOrderByGuestOrderNoForTenant(tenant, no, email, password)
		if e != nil || o == nil {
			return Owner{}, domain.ErrNotFound
		}
		owner := Owner{OrderID: o.ID, Guest: true}
		if !owner.owns(o) {
			return Owner{}, domain.ErrNotFound
		}
		return owner, nil
	}
	uid, ok := ginutil.GetUserID(c)
	if !ok {
		return Owner{}, domain.ErrNotFound
	}
	o, e := l.Orders.GetOrderByUserOrderNoForTenant(tenant, no, uid)
	if e != nil || o == nil {
		return Owner{}, domain.ErrNotFound
	}
	owner := Owner{OrderID: o.ID, UserID: uid}
	if !owner.owns(o) {
		return Owner{}, domain.ErrNotFound
	}
	return owner, nil
}
func strictLiveJSON(c *gin.Context, target any) error {
	mt, _, e := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if e != nil || mt != "application/json" {
		return domain.ErrInvalid
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 2048))
	dec.DisallowUnknownFields()
	if dec.Decode(target) != nil {
		return domain.ErrInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return domain.ErrInvalid
	}
	return nil
}
func (l *Live) quoteHTTP(guest bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		owner, e := l.ownerHTTP(c, guest)
		if e != nil {
			liveError(c, e)
			return
		}
		var req struct {
			Payer string `json:"payer"`
		}
		if e = strictLiveJSON(c, &req); e != nil {
			liveError(c, e)
			return
		}
		v, e := l.Quote(c.Request.Context(), owner, req.Payer)
		if e != nil {
			liveError(c, e)
			return
		}
		response.Success(c, v)
	}
}
func (l *Live) submitHTTP(guest bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		owner, e := l.ownerHTTP(c, guest)
		if e != nil {
			liveError(c, e)
			return
		}
		var req struct {
			InvoiceID string `json:"invoice_id"`
			Hash      string `json:"transaction_hash"`
		}
		if e = strictLiveJSON(c, &req); e != nil {
			liveError(c, e)
			return
		}
		v, e := l.Submit(c.Request.Context(), owner, req.InvoiceID, req.Hash)
		if e != nil {
			liveError(c, e)
			return
		}
		response.Success(c, statusView(v))
	}
}
func (l *Live) statusHTTP(guest bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		owner, e := l.ownerHTTP(c, guest)
		if e != nil {
			liveError(c, e)
			return
		}
		v, e := l.Status(c.Request.Context(), owner)
		if e != nil {
			liveError(c, e)
			return
		}
		response.Success(c, statusView(v))
	}
}
func statusView(s InvoiceStatus) gin.H {
	return gin.H{"invoice_id": s.InvoiceID, "payment_id": s.PaymentID, "state": s.State, "transaction_hash": s.TxHash, "safe": s.Safe, "reason": s.Reason, "block_number": s.BlockNumber}
}
func liveError(c *gin.Context, e error) {
	if c.IsAborted() {
		return
	}
	status, code := 503, "tapecommerce_unavailable"
	switch {
	case errors.Is(e, domain.ErrNotFound):
		status, code = 404, "tapecommerce_not_found"
	case errors.Is(e, domain.ErrInvalid):
		status, code = 400, "tapecommerce_invalid_request"
	case errors.Is(e, ErrExpired):
		status, code = 409, ErrExpired.Error()
	case errors.Is(e, ErrIneligible), errors.Is(e, domain.ErrReview):
		status, code = 409, "tapecommerce_order_ineligible"
	case errors.Is(e, domain.ErrConflict):
		status, code = 409, "tapecommerce_conflict"
	case errors.Is(e, domain.ErrDisabled):
		code = "tapecommerce_disabled"
	case errors.Is(e, ErrSigner):
		code = ErrSigner.Error()
	}
	c.AbortWithStatusJSON(status, gin.H{"status_code": status, "msg": code, "error_code": code})
}
