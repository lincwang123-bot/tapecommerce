package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// IP budgets also apply to authenticated email changes.
func EmailSendRateLimits(client *redis.Client) []gin.HandlerFunc {
	return []gin.HandlerFunc{
		RateLimitMiddleware(client, RateLimitRule{Prefix: "email-send:ip", WindowSeconds: 600, MaxRequests: 5, BlockSeconds: 600, MessageKey: "error.verify_code_too_frequent"}, KeyByIP),
	}
}
