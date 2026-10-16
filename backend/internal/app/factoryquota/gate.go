// Package factoryquota controls admission in a single-process stock shop.
// Already created payments, callbacks, refunds and fulfillment are never gated.
package factoryquota

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Policy struct {
	Products int64 `json:"products"`
	Orders   int64 `json:"orders"`
	Bytes    int64 `json:"bytes"`
	Expires  int64 `json:"expires"`
}

// Gate serializes admission through the existing handler, so two requests cannot
// both take the last product/order slot. One API process per shop is required.
func Gate(db *gorm.DB) gin.HandlerFunc {
	var admission sync.Mutex
	return func(c *gin.Context) {
		file := os.Getenv("FACTORY_QUOTA_FILE")
		if file == "" || c.Request.Method != "POST" {
			c.Next()
			return
		}
		p := c.Request.URL.Path
		order := p == "/api/v1/orders" || p == "/api/v1/orders/create-and-pay" || p == "/api/v1/guest/orders" || p == "/api/v1/guest/orders/create-and-pay" || p == "/api/v1/upstream/orders"
		product := p == "/api/v1/admin/products"
		storage := product || strings.HasSuffix(p, "/upload") || p == "/api/v1/admin/card-secrets/batch"
		sale := order || p == "/api/v1/wallet/recharge" || strings.HasSuffix(p, "/orders/preview")
		if !sale && !storage {
			c.Next()
			return
		}
		deny := func(code string) {
			c.AbortWithStatusJSON(503, gin.H{"status_code": 503, "error_code": code, "msg": "当前方案暂不接受新增业务，已有订单仍可处理，请联系商家"})
		}
		admission.Lock()
		defer admission.Unlock()
		var q Policy
		data, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(data, &q) != nil || q.Products < 1 || q.Orders < 1 || q.Bytes < 1 {
			deny("factory_quota_unavailable")
			return
		}
		if q.Expires > 0 && time.Now().UnixMilli() >= q.Expires {
			deny("factory_subscription_expired")
			return
		}
		if (order || product) && db == nil {
			deny("factory_quota_unavailable")
			return
		}
		if product {
			var count int64
			if db.Table("products").Where("deleted_at IS NULL").Count(&count).Error != nil {
				deny("factory_quota_unavailable")
				return
			}
			if count >= q.Products {
				deny("factory_product_quota")
				return
			}
		}
		if order {
			// Paid orders count in their UTC payment month. Pending orders reserve a
			// slot so later valid payment callbacks never need to be rejected.
			now := time.Now().UTC()
			month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			var count int64
			if db.Table("orders").Where("parent_id IS NULL AND (paid_at >= ? OR status = ?)", month, "pending_payment").Count(&count).Error != nil {
				deny("factory_quota_unavailable")
				return
			}
			if count >= q.Orders {
				deny("factory_order_quota")
				return
			}
		}
		if storage || order {
			var used int64
			for _, name := range []string{"commerce", "uploads"} {
				err = filepath.WalkDir(filepath.Join(filepath.Dir(file), name), func(p string, d fs.DirEntry, e error) error {
					if e != nil {
						return e
					}
					if d.Type()&os.ModeSymlink != 0 {
						return fs.ErrPermission
					}
					if !d.IsDir() {
						s, e := d.Info()
						if e != nil {
							return e
						}
						used += s.Size()
					}
					return nil
				})
				if err != nil {
					deny("factory_quota_unavailable")
					return
				}
			}
			// Reserve a bounded request plus DB bookkeeping; logs/backups are excluded.
			reserve := c.Request.ContentLength
			if reserve < 0 {
				deny("factory_content_length_required")
				return
			}
			reserve += 1024 * 1024
			if used+reserve > q.Bytes {
				deny("factory_storage_quota")
				return
			}
		}
		c.Next()
	}
}
