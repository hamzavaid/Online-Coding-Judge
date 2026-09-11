package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type denyLimiter struct{}

// Allow simulates a depleted shared rate limit without contacting external services.
func (denyLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, nil
}

// TestRateLimitRejectsBeforeWork verifies throttling prevents handler side effects.
func TestRateLimitRejectsBeforeWork(t *testing.T) {
	r := gin.New()
	called := false
	r.POST("/login", rateLimit(denyLimiter{}, "auth", 20), func(c *gin.Context) { called = true; c.Status(204) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/login", nil))
	if w.Code != 429 || called {
		t.Fatalf("status=%d called=%v", w.Code, called)
	}
}
