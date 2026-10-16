package middleware

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"strings"
)

// Independent dimensions prevent rotating IPs from bypassing the account quota,
// or rotating accounts from bypassing the source-IP quota. The database admission
// cap remains authoritative across instances even during Redis failure.
func RechargeRateLimits(client *redis.Client) []gin.HandlerFunc {
	return []gin.HandlerFunc{
		RateLimitMiddleware(client, RateLimitRule{Prefix: "recharge:ip", WindowSeconds: 60, MaxRequests: 20, BlockSeconds: 60}, KeyByIP),
		RateLimitMiddleware(client, RateLimitRule{Prefix: "recharge:account", WindowSeconds: 60, MaxRequests: 6, BlockSeconds: 60}, rechargeAccountKey),
	}
}
func rechargeAccountKey(c *gin.Context) string {
	if id, ok := c.Get("user_id"); ok {
		return fmt.Sprintf("user:%v", id)
	}
	id := strings.TrimSpace(readJSONField(c, "channel_user_id"))
	if id == "" {
		id = strings.TrimSpace(readJSONField(c, "telegram_user_id"))
	}
	client, _ := c.Get(channelClientIDKey)
	return fmt.Sprintf("channel:%v:%s", client, id)
}
