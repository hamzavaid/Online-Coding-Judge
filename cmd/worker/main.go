// Command worker runs a bounded pool of Redis consumers and Docker judges.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/queue"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const workerLease = time.Minute

// main reports startup or processing failure without logging source or test data.
func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

// run owns a bounded pool whose unique consumers can be replicated across processes.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return err
	}
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second})
	defer client.Close()
	if err = client.Ping(ctx).Err(); err != nil {
		return err
	}
	concurrency := 3
	if value := os.Getenv("WORKER_CONCURRENCY"); value != "" {
		concurrency, err = strconv.Atoi(value)
		if err != nil || concurrency < 1 || concurrency > 64 {
			return errors.New("WORKER_CONCURRENCY must be between 1 and 64")
		}
	}
	host, _ := os.Hostname()
	baseID := host + "-" + strconv.Itoa(os.Getpid())
	stream := queue.New(client, "judge:submissions")
	store := database.New(pool)
	factory := judge.DockerFactory{Images: map[string]string{"python": os.Getenv("PYTHON_IMAGE"), "cpp": os.Getenv("CPP_IMAGE")}}
	return judge.RunPool(ctx, concurrency, func(poolCtx context.Context, slot int) error {
		workerID := baseID + "-" + strconv.Itoa(slot)
		worker := judge.Worker{ID: workerID, Lease: workerLease, MaxDeliveries: 3, Repo: store, Queue: stream, Engine: judge.Engine{Factory: factory}}
		for {
			delivery, receiveErr := stream.Receive(poolCtx, workerID, workerLease)
			if errors.Is(receiveErr, redis.Nil) {
				continue
			}
			if receiveErr != nil {
				if poolCtx.Err() != nil {
					return nil
				}
				return receiveErr
			}
			slog.Info("judging submission", "submission_id", delivery.SubmissionID, "worker_id", workerID, "delivery_count", delivery.Deliveries)
			if handleErr := worker.Handle(poolCtx, delivery); handleErr != nil {
				if poolCtx.Err() != nil {
					return nil
				}
				if errors.Is(handleErr, judge.ErrBusy) || errors.Is(handleErr, judge.ErrStaleAttempt) {
					continue
				}
				return handleErr
			}
		}
	})
}
