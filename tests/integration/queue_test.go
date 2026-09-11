package integration

import (
	"context"
	"fmt"
	"github.com/hamzavaid/Online-Coding-Judge/internal/queue"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
	"time"
)

// TestRedisStream exercises actual consumer-group delivery and explicit acknowledgement.
func TestRedisStream(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR required")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	stream := fmt.Sprintf("test:judge:%d", time.Now().UnixNano())
	defer client.Del(ctx, stream)
	q := queue.New(client, stream)
	if e := q.Publish(ctx, "submission-1"); e != nil {
		t.Fatal(e)
	}
	m, e := q.Receive(ctx)
	if e != nil || m.SubmissionID != "submission-1" {
		t.Fatalf("%+v %v", m, e)
	}
	pending, e := client.XPending(ctx, stream, "judge-workers").Result()
	if e != nil || pending.Count != 1 {
		t.Fatalf("%+v %v", pending, e)
	}
	if e = q.Ack(ctx, m.ID); e != nil {
		t.Fatal(e)
	}
	pending, e = client.XPending(ctx, stream, "judge-workers").Result()
	if e != nil || pending.Count != 0 {
		t.Fatalf("%+v %v", pending, e)
	}
}
