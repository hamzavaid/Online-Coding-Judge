// Command worker runs the initial sequential Redis consumer and Docker judge.
package main

import (
	"context"
	"errors"
	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/queue"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// main reports startup or processing failure without logging source or test data.
func main() {
	if e := run(); e != nil {
		slog.Error("worker stopped", "error", e)
		os.Exit(1)
	}
}

// run owns a single consumer and cancels its active sandbox on process termination.
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
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second})
	defer client.Close()
	if e = client.Ping(ctx).Err(); e != nil {
		return e
	}
	q := queue.New(client, "judge:submissions")
	w := judge.Worker{Repo: database.New(pool), Queue: q, Engine: judge.Engine{Factory: judge.DockerFactory{Images: map[string]string{"python": os.Getenv("PYTHON_IMAGE"), "cpp": os.Getenv("CPP_IMAGE")}}}}
	for ctx.Err() == nil {
		message, e := q.Receive(ctx)
		if errors.Is(e, redis.Nil) {
			continue
		}
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		slog.Info("judging submission", "submission_id", message.SubmissionID)
		if e = w.Handle(ctx, message.SubmissionID, message.ID); e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		slog.Info("submission persisted", "submission_id", message.SubmissionID)
	}
	return nil
}
