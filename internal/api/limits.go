package api

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
)

// Limiter enforces expiring counters shared by API processes.
type Limiter interface {
	Allow(context.Context, string, int, time.Duration) (bool, error)
}

// rateLimit fails closed for writes when coordination is unavailable, leaving read routes unaffected.
func rateLimit(limiter Limiter, bucket string, limit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.ClientIP()
		if user, ok := c.Get("user"); ok {
			key = user.(database.User).ID
		}
		allowed, e := limiter.Allow(c.Request.Context(), bucket+":"+key, limit, time.Minute)
		if e != nil {
			fail(c, 503)
			return
		}
		if !allowed {
			c.Header("Retry-After", "60")
			fail(c, 429)
			return
		}
		c.Next()
	}
}
