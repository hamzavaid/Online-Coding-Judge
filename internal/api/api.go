// Package api exposes bounded REST handlers and role-based access control.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hamzavaid/Online-Coding-Judge/internal/auth"
	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
	"github.com/jackc/pgx/v5/pgconn"
)

// New constructs the API; request bodies and database calls have hard bounds.
func New(s *database.Store, limiters ...Limiter) http.Handler {
	r := gin.New()
	// Forwarded headers are untrusted unless a deployment explicitly configures its proxy boundary.
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery())
	r.Use(func(c *gin.Context) {
		if strings.HasSuffix(c.Request.URL.Path, "/events") {
			c.Header("X-Content-Type-Options", "nosniff")
			c.Header("Cache-Control", "no-store")
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/ready", func(c *gin.Context) {
		if s.Pool.Ping(c.Request.Context()) != nil {
			fail(c, 503)
			return
		}
		c.Status(204)
	})
	authLimit := func(c *gin.Context) { c.Next() }
	submissionLimit := authLimit
	if len(limiters) > 0 {
		authLimit = rateLimit(limiters[0], "auth", 20)
		submissionLimit = rateLimit(limiters[0], "submission", 10)
	}
	r.POST("/v1/auth/register", authLimit, func(c *gin.Context) {
		var v struct {
			Username string `json:"username"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !decode(c, &v, 4096) {
			return
		}
		v.Email = strings.ToLower(strings.TrimSpace(v.Email))
		address, e := mail.ParseAddress(v.Email)
		if e != nil || address.Address != v.Email || len(v.Email) > 254 || len(v.Username) < 3 || len(v.Username) > 40 {
			fail(c, 400)
			return
		}
		hash, e := auth.HashPassword(v.Password)
		if e != nil {
			fail(c, 400)
			return
		}
		var id string
		e = s.Pool.QueryRow(c.Request.Context(), "INSERT INTO users(username,email,password_hash) VALUES($1,$2,$3) RETURNING id", v.Username, v.Email, hash).Scan(&id)
		if e != nil {
			dbFail(c, e)
			return
		}
		c.JSON(201, gin.H{"id": id})
	})
	r.POST("/v1/auth/login", authLimit, func(c *gin.Context) {
		var v struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !decode(c, &v, 4096) {
			return
		}
		var id, hash string
		e := s.Pool.QueryRow(c.Request.Context(), "SELECT id,password_hash FROM users WHERE email=$1", strings.ToLower(strings.TrimSpace(v.Email))).Scan(&id, &hash)
		if e != nil || !auth.Verify(hash, v.Password) {
			fail(c, 401)
			return
		}
		token, e := auth.Token()
		if e != nil {
			fail(c, 500)
			return
		}
		_, e = s.Pool.Exec(c.Request.Context(), "INSERT INTO sessions VALUES($1,$2,$3)", auth.Digest(token), id, time.Now().Add(24*time.Hour))
		if e != nil {
			fail(c, 500)
			return
		}
		c.JSON(200, gin.H{"token": token, "expires_in": 86400})
	})
	authenticate := func(c *gin.Context) {
		token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		var u database.User
		if len(token) != 64 {
			fail(c, 401)
			return
		}
		e := s.Pool.QueryRow(c.Request.Context(), "SELECT u.id,u.username,u.email,u.role FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()", auth.Digest(token)).Scan(&u.ID, &u.Username, &u.Email, &u.Role)
		if e != nil {
			fail(c, 401)
			return
		}
		c.Set("user", u)
		c.Next()
	}
	r.GET("/v1/problems", func(c *gin.Context) {
		rows, e := s.Pool.Query(c.Request.Context(), "SELECT id,title,slug,difficulty FROM problems WHERE status='published' ORDER BY slug LIMIT 100")
		if e != nil {
			fail(c, 500)
			return
		}
		defer rows.Close()
		items := []gin.H{}
		for rows.Next() {
			var id, title, slug, difficulty string
			if rows.Scan(&id, &title, &slug, &difficulty) != nil {
				fail(c, 500)
				return
			}
			items = append(items, gin.H{"id": id, "title": title, "slug": slug, "difficulty": difficulty})
		}
		if rows.Err() != nil {
			fail(c, 500)
			return
		}
		c.JSON(200, gin.H{"problems": items})
	})
	r.GET("/v1/problems/:id", func(c *gin.Context) {
		p, e := s.Problem(c.Request.Context(), c.Param("id"))
		if e != nil || p.Status != "published" {
			fail(c, 404)
			return
		}
		c.JSON(200, p.Public())
	})
	private := r.Group("/v1", authenticate)
	private.GET("/users/me", func(c *gin.Context) { c.JSON(200, c.MustGet("user")) })
	private.POST("/auth/logout", func(c *gin.Context) {
		_, e := s.Pool.Exec(c.Request.Context(), "DELETE FROM sessions WHERE token_hash=$1", auth.Digest(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")))
		if e != nil {
			fail(c, 500)
			return
		}
		c.Status(204)
	})
	private.POST("/submissions", submissionLimit, func(c *gin.Context) {
		var v struct {
			ProblemID string `json:"problem_id"`
			Language  string `json:"language_id"`
			Source    string `json:"source_code"`
		}
		if !decode(c, &v, 400000) {
			return
		}
		sub, e := s.CreateSubmission(c.Request.Context(), c.MustGet("user").(database.User).ID, v.ProblemID, v.Language, v.Source)
		if e != nil {
			fail(c, 400)
			return
		}
		c.JSON(202, sub)
	})
	history := func(c *gin.Context) {
		items, e := s.History(c.Request.Context(), c.MustGet("user").(database.User).ID, c.Param("id"))
		if e != nil {
			fail(c, 500)
			return
		}
		if c.Param("id") != "" {
			if len(items) == 0 {
				fail(c, 404)
				return
			}
			c.JSON(200, items[0])
			return
		}
		c.JSON(200, gin.H{"submissions": items})
	}
	private.GET("/submissions/:id", history)
	private.GET("/submissions/:id/events", func(c *gin.Context) {
		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			fail(c, 500)
			return
		}
		user := c.MustGet("user").(database.User)
		last := ""
		for {
			items, err := s.History(c.Request.Context(), user.ID, c.Param("id"))
			if err != nil {
				fail(c, 500)
				return
			}
			if len(items) == 0 {
				fail(c, 404)
				return
			}
			payload, err := json.Marshal(items[0])
			if err != nil {
				fail(c, 500)
				return
			}
			if string(payload) != last {
				c.Header("Content-Type", "text/event-stream")
				c.Header("Connection", "keep-alive")
				_, _ = c.Writer.Write([]byte("event: submission\ndata: " + string(payload) + "\n\n"))
				flusher.Flush()
				last = string(payload)
			}
			if items[0].Status == "FINAL" || items[0].Status == "FAILED_INTERNAL" {
				return
			}
			select {
			case <-c.Request.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	})
	private.GET("/users/me/submissions", history)
	admin := private.Group("/admin", func(c *gin.Context) {
		if c.MustGet("user").(database.User).Role != "admin" {
			fail(c, 403)
			return
		}
		c.Next()
	})
	save := func(c *gin.Context) {
		var p problems.Problem
		if !decode(c, &p, 7000000) {
			return
		}
		p.ID = c.Param("id")
		if p.Validate() != nil {
			fail(c, 400)
			return
		}
		p, e := s.SaveProblem(c.Request.Context(), p)
		if e != nil {
			dbFail(c, e)
			return
		}
		status := 200
		if c.Request.Method == "POST" {
			status = 201
		}
		c.JSON(status, p)
	}
	admin.POST("/problems", save)
	admin.PUT("/problems/:id", save)
	admin.GET("/problems/:id", func(c *gin.Context) {
		p, e := s.Problem(c.Request.Context(), c.Param("id"))
		if e != nil {
			fail(c, 404)
			return
		}
		c.JSON(200, p)
	})
	admin.DELETE("/problems/:id", func(c *gin.Context) {
		tag, e := s.Pool.Exec(c.Request.Context(), "UPDATE problems SET status='disabled' WHERE id=$1", c.Param("id"))
		if e != nil || tag.RowsAffected() == 0 {
			fail(c, 404)
			return
		}
		c.Status(204)
	})
	return r
}

// decode rejects oversized, unknown, and trailing fields to prevent privilege mass assignment.
func decode(c *gin.Context, v any, limit int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		fail(c, 400)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		fail(c, 400)
		return false
	}
	return true
}

// fail emits only generic messages, never database errors or hidden test material.
func fail(c *gin.Context, status int) {
	c.AbortWithStatusJSON(status, gin.H{"error": http.StatusText(status)})
}

// dbFail maps uniqueness violations while withholding SQL details.
func dbFail(c *gin.Context, e error) {
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "23505" {
		fail(c, 409)
		return
	}
	fail(c, 500)
}
