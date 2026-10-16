package middleware

import (
	"os"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

var tapeStoredSubmission = regexp.MustCompile(`^/api/v1/(?:guest/)?orders/[^/]+/tapecommerce/submit$`)

var storedChainSubmission = regexp.MustCompile(`^/api/v1/(?:payments/[1-9][0-9]*|guest/payments/[1-9][0-9]*|wallet/recharge/payments/[1-9][0-9]*)/submit-tx$`)

// Production is closed by default until payment and redemption are commissioned.
func CommerceLaunchGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		factoryAllowed := FactorySalesAllowed(c)
		if !factoryAllowed && factoryNewSale(c) {
			c.AbortWithStatusJSON(503, gin.H{"status_code": 503, "msg": "商店暂不接受新订单，已有订单仍可查询处理", "error_code": "factory_sales_paused"})
			return
		}
		enabled := os.Getenv("STORE_ENV") != "production" || os.Getenv("STORE_COMMERCE_ENABLED") == "true"
		if c.Request.URL.Path == "/api/v1/store-status" && c.Request.Method == "GET" {
			c.AbortWithStatusJSON(200, gin.H{"status_code": 0, "data": gin.H{"commerce_enabled": enabled && factoryAllowed, "notice": func() string {
				if enabled && factoryAllowed {
					return ""
				}
				return "商城筹备中，充值与购买暂未开放。已有兑换码请前往核销站。"
			}()}})
			return
		}
		path := c.Request.URL.Path
		// A checkout pause must still reconcile payments already sent. This only
		// reaches the existing verified handler; it cannot create a payment or
		// bypass its HMAC, gateway query, or existing-order checks.
		paymentNotification := c.Request.Method == "POST" && path == "/api/v1/payments/webhook/nowpayments"
		// Login remains available while checkout is closed, including its second
		// factor. The normal challenge handler and limiter still verify it.
		loginChallenge := c.Request.Method == "POST" && path == "/api/v1/auth/login/verify-2fa"
		if !enabled && !paymentNotification && !loginChallenge && !(c.Request.Method == "POST" && (storedChainSubmission.MatchString(path) || tapeStoredSubmission.MatchString(path))) && c.Request.Method != "GET" && c.Request.Method != "HEAD" && c.Request.Method != "OPTIONS" && !strings.HasPrefix(path, "/api/v1/admin/") && path != "/api/v1/auth/login" && path != "/api/v1/auth/logout" {
			c.AbortWithStatusJSON(503, gin.H{"status_code": 503, "msg": "商城筹备中，充值与购买暂未开放", "error_code": "commerce_not_open"})
			return
		}
		c.Next()
	}
}
