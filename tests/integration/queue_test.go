package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/queue"
	"github.com/redis/go-redis/v9"
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
	m, e := q.Receive(ctx, "worker-one", time.Minute)
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

// TestRedisRateLimit verifies atomic count/expiry without permitting an extra request.
func TestRedisRateLimit(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR required")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	key := fmt.Sprintf("test:limit:%d", time.Now().UnixNano())
	defer client.Del(ctx, "judge:rate:"+key)
	q := queue.New(client, "unused")
	for i := 0; i < 4; i++ {
		ok, e := q.Allow(ctx, key, 3, time.Minute)
		if e != nil || ok != (i < 3) {
			t.Fatalf("request %d: %v %v", i, ok, e)
		}
	}
	ttl, e := client.TTL(ctx, "judge:rate:"+key).Result()
	if e != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("missing expiry %s %v", ttl, e)
	}
}

// TestRedisReclaimAndDeadLetter transfers an abandoned message to another consumer, then atomically DLQs it.
func TestRedisReclaimAndDeadLetter(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR required")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	stream := fmt.Sprintf("test:reclaim:%d", time.Now().UnixNano())
	defer client.Del(ctx, stream, stream+":dead")
	q := queue.New(client, stream)
	if err := q.Publish(ctx, "submission-1"); err != nil {
		t.Fatal(err)
	}
	first, err := q.Receive(ctx, "worker-a", time.Hour)
	if err != nil || first.Deliveries != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	time.Sleep(30 * time.Millisecond)
	second, err := q.Receive(ctx, "worker-b", 10*time.Millisecond)
	if err != nil || second.ID != first.ID || second.Deliveries < 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if err = q.DeadLetter(ctx, second, "attempts_exhausted"); err != nil {
		t.Fatal(err)
	}
	pending, err := client.XPending(ctx, stream, "judge-workers").Result()
	if err != nil || pending.Count != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	dead, err := client.XRange(ctx, stream+":dead", "-", "+").Result()
	if err != nil || len(dead) != 1 || dead[0].Values["submission_id"] != "submission-1" {
		t.Fatalf("dead=%+v err=%v", dead, err)
	}
}
