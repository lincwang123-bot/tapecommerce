package middleware

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFactoryMerchantCannotReplaceQualifiedRuntime(t *testing.T) {
	t.Setenv("FACTORY_SHOP_ID", "shop_a")
	t.Setenv("FACTORY_HOST", "a.example.com")
	t.Setenv("FACTORY_PROXY_TOKEN", strings.Repeat("a", 40))
	for _, path := range []string{"/api/v1/admin/system/update/start", "/api/v1/admin/system/update/rollback"} {
		r := gin.New()
		r.Use(FactoryProxyGuard())
		r.POST(path, func(c *gin.Context) { t.Error("runtime mutation reached handler") })
		q := httptest.NewRequest("POST", "https://a.example.com"+path, strings.NewReader(`{"force":true}`))
		q.Header.Set("X-Factory-Shop-Id", "shop_a")
		q.Header.Set("X-Factory-Proxy-Token", strings.Repeat("a", 40))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		if w.Code != 403 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}

func TestFactoryPauseOnlyNewSales(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "true")
	f := filepath.Join(t.TempDir(), "sales.json")
	t.Setenv("FACTORY_SALES_FILE", f)
	for _, p := range []string{"/api/v1/orders/create-and-pay", "/api/v1/wallet/recharge", "/api/v1/upstream/orders"} {
		r := gin.New()
		r.Use(CommerceLaunchGate())
		r.POST(p, func(c *gin.Context) { c.Status(204) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		if w.Code != 503 {
			t.Fatalf("%s not blocked: %d", p, w.Code)
		}
	}
	for _, p := range []string{"/api/v1/payments/1/capture", "/api/v1/payments/webhook/nowpayments", "/api/v1/auth/register", "/api/v1/admin/orders/1/refund"} {
		r := gin.New()
		r.Use(CommerceLaunchGate())
		r.POST(p, func(c *gin.Context) { c.Status(204) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		if w.Code != 204 {
			t.Fatalf("%s incorrectly blocked: %d", p, w.Code)
		}
	}
	if err := os.WriteFile(f, []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(CommerceLaunchGate())
	r.POST("/api/v1/orders", func(c *gin.Context) { c.Status(204) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/orders", nil))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}

func TestFactoryProxyRequiresOwnIdentityAndCanonicalHost(t *testing.T) {
	t.Setenv("FACTORY_SHOP_ID", "shop_a")
	t.Setenv("FACTORY_HOST", "a.example.com")
	t.Setenv("FACTORY_PROXY_TOKEN", strings.Repeat("a", 40))
	r := gin.New()
	r.Use(FactoryProxyGuard())
	r.GET("/", func(c *gin.Context) {
		if c.GetHeader("X-Factory-Proxy-Token") != "" {
			t.Fatal("proxy token leaked past guard")
		}
		c.Status(200)
	})
	for _, tc := range []struct {
		name, host, shop, token, forwarded string
		code                               int
	}{
		{"normal", "a.example.com", "shop_a", strings.Repeat("a", 40), "", 200},
		{"direct", "a.example.com", "", "", "", 421},
		{"other shop", "a.example.com", "shop_b", strings.Repeat("b", 40), "", 421},
		{"wrong host", "b.example.com", "shop_a", strings.Repeat("a", 40), "", 421},
		{"forwarded", "a.example.com", "shop_a", strings.Repeat("a", 40), "b.example.com", 421},
		{"multi host", "a.example.com,b.example.com", "shop_a", strings.Repeat("a", 40), "", 421},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := httptest.NewRequest("GET", "http://"+"example.com/", nil)
			q.Host = tc.host
			q.Header.Set("X-Factory-Shop-Id", tc.shop)
			q.Header.Set("X-Factory-Proxy-Token", tc.token)
			if tc.forwarded != "" {
				q.Header.Set("X-Forwarded-Host", tc.forwarded)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, q)
			if w.Code != tc.code {
				t.Fatalf("got %d", w.Code)
			}
		})
	}
}

func TestFactoryAuthenticatedClientIP(t *testing.T) {
	t.Setenv("FACTORY_SHOP_ID", "shop_a")
	t.Setenv("FACTORY_HOST", "a.example.com")
	t.Setenv("FACTORY_PROXY_TOKEN", strings.Repeat("a", 40))
	t.Setenv("FACTORY_TRUST_CLIENT_IP", "1")
	for _, tc := range []struct {
		name  string
		ip    []string
		token string
		want  int
	}{
		{"ipv4", []string{"198.51.100.1"}, strings.Repeat("a", 40), 200},
		{"ipv6", []string{"2001:db8::1"}, strings.Repeat("a", 40), 200},
		{"missing", nil, strings.Repeat("a", 40), 421},
		{"forged", []string{"198.51.100.1"}, "invalid", 421},
		{"list", []string{"198.51.100.1, 198.51.100.2"}, strings.Repeat("a", 40), 421},
		{"duplicates", []string{"198.51.100.1", "198.51.100.2"}, strings.Repeat("a", 40), 421},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			_ = r.SetTrustedProxies(nil)
			r.Use(FactoryProxyGuard())
			r.GET("/", func(c *gin.Context) {
				if c.ClientIP() != tc.ip[0] || c.GetHeader("X-Factory-Client-IP") != "" {
					t.Fatal("trusted address not scoped/removed")
				}
				c.Status(200)
			})
			req := httptest.NewRequest("GET", "http://a.example.com/", nil)
			req.Header.Set("X-Factory-Shop-Id", "shop_a")
			req.Header.Set("X-Factory-Proxy-Token", tc.token)
			for _, v := range tc.ip {
				req.Header.Add("X-Factory-Client-IP", v)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}
