package middleware

import (
	"crypto/subtle"
	"encoding/json"
	resellercontract "github.com/dujiao-next/internal/modules/reseller/contract"
	"github.com/gin-gonic/gin"
	"net"
	"os"
	"strings"
)

// FactorySalesAllowed pauses only new sale intents. Existing callbacks, capture,
// refunds, delivery, account access and queries retain their normal authorization.
func FactorySalesAllowed(c *gin.Context) bool {
	file := os.Getenv("FACTORY_SALES_FILE")
	if file == "" {
		return true
	}
	var state struct {
		Enabled bool `json:"enabled"`
	}
	data, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(data, &state) != nil || !state.Enabled {
		return false
	}
	return true
}
func factoryNewSale(c *gin.Context) bool {
	if c.Request.Method != "POST" {
		return false
	}
	p := c.Request.URL.Path
	return p == "/api/v1/orders" || p == "/api/v1/orders/create-and-pay" || p == "/api/v1/guest/orders" || p == "/api/v1/guest/orders/create-and-pay" || p == "/api/v1/wallet/recharge" || p == "/api/v1/upstream/orders" || strings.HasSuffix(p, "/orders/preview")
}

// Per-instance origin authentication is independent of customer/admin tokens.
// API ports are not published; no request header can select a different shop.
func FactoryProxyGuard() gin.HandlerFunc {
	id, host, token := os.Getenv("FACTORY_SHOP_ID"), os.Getenv("FACTORY_HOST"), os.Getenv("FACTORY_PROXY_TOKEN")
	trustClientIP := os.Getenv("FACTORY_TRUST_CLIENT_IP") == "1"
	return func(c *gin.Context) {
		if id == "" {
			c.Next()
			return
		}
		ok := len(token) >= 32 && host != "" && resellercontract.RequestHost(c.Request.Host) == host
		for k := range c.Request.Header {
			lower := strings.ToLower(k)
			if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || lower == "x-real-ip" {
				ok = false
			}
		}
		values := c.Request.Header.Values("X-Factory-Proxy-Token")
		shops := c.Request.Header.Values("X-Factory-Shop-Id")
		ok = ok && len(values) == 1 && len(shops) == 1
		if !ok || shops[0] != id || subtle.ConstantTimeCompare([]byte(values[0]), []byte(token)) != 1 {
			c.AbortWithStatusJSON(421, gin.H{"status_code": 421, "msg": "Untrusted storefront origin"})
			return
		}
		if trustClientIP {
			forwarded := c.Request.Header.Values("X-Factory-Client-IP")
			if len(forwarded) != 1 || net.ParseIP(forwarded[0]) == nil {
				c.AbortWithStatusJSON(421, gin.H{"status_code": 421, "msg": "Untrusted storefront client address"})
				return
			}
			c.Request.RemoteAddr = net.JoinHostPort(forwarded[0], "0")
		}
		c.Request.Header.Del("X-Factory-Client-IP")
		c.Request.Header.Del("X-Factory-Proxy-Token")
		c.Request.Header.Del("X-Factory-Shop-Id")
		// Hosted runtime changes must use the qualified platform release path.
		if c.Request.Method == "POST" && (c.Request.URL.Path == "/api/v1/admin/system/update/start" || c.Request.URL.Path == "/api/v1/admin/system/update/rollback") {
			c.AbortWithStatusJSON(403, gin.H{"status_code": 403, "msg": "托管分站版本由主站管理，请通过平台执行更新或回退"})
			return
		}
		c.Next()
	}
}
