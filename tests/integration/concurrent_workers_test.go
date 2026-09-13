package integration

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/outbox"
	"github.com/hamzavaid/Online-Coding-Judge/internal/queue"
	"github.com/redis/go-redis/v9"
)

type acceptedFactory struct{}

func (acceptedFactory) Open(context.Context, judge.Job) (judge.Sandbox, error) {
	return acceptedSandbox{}, nil
}

type acceptedSandbox struct{}

func (acceptedSandbox) Compile(context.Context) (judge.Outcome, error) { return judge.Outcome{}, nil }
func (acceptedSandbox) Run(context.Context, string) (judge.Outcome, error) {
	time.Sleep(20 * time.Millisecond)
	return judge.Outcome{Output: "ok"}, nil
}
func (acceptedSandbox) Close() error { return nil }

// TestThreeWorkersProcessConcurrently exercises distinct Redis consumers and fenced PostgreSQL attempts.
func TestThreeWorkersProcessConcurrently(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR required")
	}
	store := database.New(testDB(t))
	ctx := context.Background()
	_, first := seededSubmission(t, store)
	ids := []string{first}
	for i := 1; i < 3; i++ {
		var user string
		if err := store.Pool.QueryRow(ctx, "SELECT user_id FROM submissions WHERE id=$1", first).Scan(&user); err != nil {
			t.Fatal(err)
		}
		var problem string
		if err := store.Pool.QueryRow(ctx, "SELECT problem_id FROM submissions WHERE id=$1", first).Scan(&problem); err != nil {
			t.Fatal(err)
		}
		sub, err := store.CreateSubmission(ctx, user, problem, "python", "print('ok')")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sub.ID)
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	streamName := fmt.Sprintf("test:workers:%d", time.Now().UnixNano())
	defer client.Del(ctx, streamName, streamName+":dead")
	stream := queue.New(client, streamName)
	publisher := outbox.Publisher{ID: "publisher", Store: store, Queue: stream, Lease: time.Minute, BatchSize: 10}
	if n, err := publisher.PublishBatch(ctx); err != nil || n != 3 {
		t.Fatalf("published=%d err=%v", n, err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			workerID := fmt.Sprintf("worker-%d", i)
			delivery, err := stream.Receive(ctx, workerID, time.Minute)
			if err == nil {
				worker := judge.Worker{ID: workerID, Lease: time.Minute, MaxDeliveries: 3, Repo: store, Queue: stream, Engine: judge.Engine{Factory: acceptedFactory{}}}
				err = worker.Handle(ctx, delivery)
			}
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var final int
	if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM submissions WHERE id=ANY($1) AND status='FINAL' AND verdict='ACCEPTED'", ids).Scan(&final); err != nil || final != 3 {
		t.Fatalf("final=%d err=%v", final, err)
	}
}
