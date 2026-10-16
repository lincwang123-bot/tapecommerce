package middleware

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func rechargeRouter(channel bool) *gin.Engine {
	r := gin.New()
	r.SetTrustedProxies(nil)
	r.Use(func(c *gin.Context) {
		if channel {
			c.Set(channelClientIDKey, 1)
		} else {
			c.Set("user_id", c.GetHeader("Test-User"))
		}
	})
	r.POST("/wallet/recharge", append(RechargeRateLimits(nil), func(c *gin.Context) { c.Status(204) })...)
	return r
}
func rechargeRequest(r *gin.Engine, user, ip, body string) int {
	req := httptest.NewRequest("POST", "/wallet/recharge", strings.NewReader(body))
	req.RemoteAddr = ip + ":1234"
	req.Header.Set("Test-User", user)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}
func TestRechargeAccountLimitSurvivesIPRotation(t *testing.T) {
	for _, channel := range []bool{false, true} {
		r := rechargeRouter(channel)
		for i := range 7 {
			body := `{"channel_user_id":"42"}`
			if i%2 == 1 {
				body = `{"telegram_user_id":"42"}`
			}
			got := rechargeRequest(r, "42", fmt.Sprintf("192.0.2.%d", i+1), body)
			want := 204
			if i == 6 {
				want = 429
			}
			if got != want {
				t.Fatalf("channel %v request %d got %d", channel, i, got)
			}
		}
	}
}
func TestRechargeIPLimitSurvivesAccountRotation(t *testing.T) {
	r := rechargeRouter(false)
	for i := range 21 {
		got := rechargeRequest(r, fmt.Sprint(i), "192.0.2.1", `{}`)
		want := 204
		if i == 20 {
			want = 429
		}
		if got != want {
			t.Fatalf("request %d got %d", i, got)
		}
	}
}
