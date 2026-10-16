package middleware

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestProductionCommerceClosedByDefault(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CommerceLaunchGate())
	for _, p := range []string{"/api/v1/orders/create-and-pay", "/api/v1/wallet/recharge", "/api/v1/upstream/orders", "/api/v1/payments/webhook/nowpayments", "/api/v1/auth/register", "/api/v1/admin/login"} {
		r.POST(p, func(c *gin.Context) { c.Status(204) })
	}
	for _, p := range []string{"/api/v1/orders/create-and-pay", "/api/v1/wallet/recharge", "/api/v1/upstream/orders", "/api/v1/auth/register"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		if w.Code != 503 {
			t.Fatal(p, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/admin/login", nil))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}

// Closing checkout must not discard a gateway notification for money already
// sent. The ordinary webhook handler still verifies the signature, queries the
// gateway, and matches an existing payment; the gate only dispatches to it.
func TestClosedCommerceDispatchesOnlySupportedPaymentNotification(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "false")
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/v1/payments/webhook/nowpayments?channel_id=1", 400},
		{"POST", "/api/v1/payments/webhook/nowpayments/extra", 503},
		{"POST", "/api/v1/payments/webhook/stripe", 503},
		{"PUT", "/api/v1/payments/webhook/nowpayments", 503},
		{"POST", "/api/v1/payments", 503},
		{"POST", "/api/v1/payments/1/capture", 503},
		{"POST", "/api/v1/wallet/recharge", 503},
		{"POST", "/api/v1/upstream/orders", 503},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := gin.New()
			r.Use(CommerceLaunchGate())
			called := false
			r.NoRoute(func(c *gin.Context) {
				called = true
				c.Status(400) // Missing signature is still rejected by the next handler.
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want || called != (tc.want == 400) {
				t.Fatalf("status=%d downstream_called=%v, want status=%d", w.Code, called, tc.want)
			}
		})
	}
}

func TestClosedCommerceAllowsLoginChallengeButNotAccountChanges(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "false")
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/v1/auth/login/verify-2fa", 401},
		{"POST", "/api/v1/auth/login/verify-2fa/extra", 503},
		{"PUT", "/api/v1/auth/login/verify-2fa", 503},
		{"POST", "/api/v1/me/2fa/setup", 503},
		{"POST", "/api/v1/me/2fa/disable", 503},
		{"POST", "/api/v1/auth/send-verify-code", 503},
		{"POST", "/api/v1/auth/forgot-password", 503},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := gin.New()
			r.Use(CommerceLaunchGate())
			called := false
			r.NoRoute(func(c *gin.Context) {
				called = true
				c.Status(401) // Only dispatch: authentication still rejects an invalid challenge.
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want || called != (tc.want == 401) {
				t.Fatalf("status=%d downstream_called=%v, want status=%d", w.Code, called, tc.want)
			}
		})
	}
}

func TestClosedCommerceDispatchesOnlyExactTransactionSubmission(t *testing.T) {
	t.Setenv("STORE_ENV", "production")
	t.Setenv("STORE_COMMERCE_ENABLED", "false")
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/v1/payments/1/submit-tx", 401}, {"POST", "/api/v1/guest/payments/2/submit-tx", 401}, {"POST", "/api/v1/wallet/recharge/payments/3/submit-tx", 401},
		{"PUT", "/api/v1/payments/1/submit-tx", 503}, {"POST", "/api/v1/payments/0/submit-tx", 503}, {"POST", "/api/v1/payments/01/submit-tx", 503}, {"POST", "/api/v1/payments/1/submit-tx/extra", 503}, {"POST", "/api/v1/payments/1/capture", 503},
	} {
		r := gin.New()
		r.Use(CommerceLaunchGate())
		r.NoRoute(func(c *gin.Context) { c.Status(401) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Fatalf("%s %s status=%d", tc.method, tc.path, w.Code)
		}
	}
}
