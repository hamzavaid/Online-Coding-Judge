// Command api serves the public REST API without executing submitted code.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/api"
	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
	"github.com/hamzavaid/Online-Coding-Judge/internal/outbox"
	"github.com/hamzavaid/Online-Coding-Judge/internal/queue"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// main opens dependencies and drains requests on termination.
func main() {
	if e := run(); e != nil {
		slog.Error("API stopped", "error", e)
		os.Exit(1)
	}
}

// run owns the connection pool and HTTP server lifecycle.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer pool.Close()
	if e = pool.Ping(ctx); e != nil {
		return e
	}
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: redisAddr, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, MaxRetries: -1})
	defer client.Close()
	store := database.New(pool)
	stream := queue.New(client, "judge:submissions")
	publisher := &outbox.Publisher{ID: processID("api"), Store: store, Queue: stream, Lease: 30 * time.Second, BatchSize: 50}
	go publishOutbox(ctx, store, publisher)
	// Handler contexts bound ordinary requests; no server write deadline is set so SSE may outlive a judging run.
	server := &http.Server{Addr: addr, Handler: api.New(store, stream), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case e = <-done:
		return e
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// publishOutbox continually transfers committed events and repairs missing queued events.
func publishOutbox(ctx context.Context, store *database.Store, publisher *outbox.Publisher) {
	ticker := time.NewTicker(250 * time.Millisecond)
	reconcile := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	defer reconcile.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcile.C:
			if _, err := store.ReconcileOutbox(ctx); err != nil {
				slog.Warn("outbox reconciliation failed", "error", err)
			}
		case <-ticker.C:
			if _, err := publisher.PublishBatch(ctx); err != nil {
				slog.Warn("outbox publication failed", "error", err)
			}
		}
	}
}

// processID builds a stable per-process identifier without exposing host secrets.
func processID(prefix string) string {
	host, _ := os.Hostname()
	return prefix + "-" + host + "-" + strconv.Itoa(os.Getpid())
}
