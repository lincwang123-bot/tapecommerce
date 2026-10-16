package supplierhttp

import (
	"io"
	"strings"

	suppliercontract "github.com/dujiao-next/internal/modules/suppliermanagement/contract"
	"github.com/dujiao-next/internal/platform/http/ginutil"
	"github.com/gin-gonic/gin"
)

// RegisterAdminRoutes must be mounted behind the existing administrator JWT and
// RBAC middleware. The handler also requires the authenticated super-admin flag.
func RegisterAdminRoutes(group gin.IRoutes, proxy suppliercontract.Proxy) {
	for _, route := range suppliercontract.Routes {
		route := route
		group.Handle(route.Method, route.Path, func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			rawID, exists := c.Get("admin_id")
			id, validID := rawID.(uint)
			if !exists || !validID || id == 0 {
				reject(c, 401, "unauthorized")
				return
			}
			if !ginutil.IsSuperAdmin(c) {
				reject(c, 403, "super_admin_required")
				return
			}
			if c.Request.URL.RawQuery != "" {
				reject(c, 400, "query_not_supported")
				return
			}
			path := route.Path
			if strings.Contains(path, ":id") {
				id := c.Param("id")
				if !suppliercontract.ValidID(id) {
					reject(c, 400, "invalid_input")
					return
				}
				path = strings.Replace(path, ":id", id, 1)
			}
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, suppliercontract.MaxRequestBytes+1))
			if err != nil || len(body) > suppliercontract.MaxRequestBytes {
				reject(c, 400, "invalid_input")
				return
			}
			data, failure := proxy.Forward(c.Request.Context(), route.Method, path, body)
			if failure != nil {
				reject(c, failure.Status, failure.Code)
				return
			}
			c.JSON(200, gin.H{"status_code": 0, "msg": "success", "data": data})
		})
	}
}
func reject(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"status_code": status, "error_code": code, "msg": "上游管理请求未完成，请检查权限、配置或刷新后重试"})
}
