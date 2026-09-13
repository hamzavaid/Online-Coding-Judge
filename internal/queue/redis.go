// Package queue implements the single-worker Redis Streams transport.
package queue

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/redis/go-redis/v9"
)

// Message contains identifiers only; source and hidden tests remain in PostgreSQL.
type Message = judge.Delivery

// Stream supports uniquely named consumers in a shared at-least-once group.
type Stream struct {
	client *redis.Client
	name   string
}

// New binds a stream without changing server state.
func New(client *redis.Client, name string) *Stream { return &Stream{client: client, name: name} }

// Publish appends a submission ID after the submission transaction commits.
func (q *Stream) Publish(ctx context.Context, id string) error {
	return q.client.XAdd(ctx, &redis.XAddArgs{Stream: q.name, Values: map[string]any{"submission_id": id}}).Err()
}

// Receive creates the group if needed, then blocks for at most one second for new work.
func (q *Stream) Receive(ctx context.Context, consumer string, reclaimAfter time.Duration) (Message, error) {
	if consumer == "" || reclaimAfter <= 0 {
		return Message{}, errors.New("invalid consumer lease")
	}
	e := q.client.XGroupCreateMkStream(ctx, q.name, "judge-workers", "0").Err()
	if e != nil && !strings.HasPrefix(e.Error(), "BUSYGROUP") {
		return Message{}, e
	}
	reclaimed, _, e := q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: q.name, Group: "judge-workers", Consumer: consumer, MinIdle: reclaimAfter, Start: "0-0", Count: 1}).Result()
	if e != nil && !errors.Is(e, redis.Nil) {
		return Message{}, e
	}
	if len(reclaimed) > 0 {
		return q.delivery(ctx, reclaimed[0])
	}
	streams, e := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "judge-workers", Consumer: consumer, Streams: []string{q.name, ">"}, Count: 1, Block: time.Second}).Result()
	if e != nil {
		return Message{}, e
	}
	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		return Message{}, redis.Nil
	}
	return q.delivery(ctx, streams[0].Messages[0])
}

// delivery adds Redis' authoritative pending-delivery count to a stream message.
func (q *Stream) delivery(ctx context.Context, m redis.XMessage) (Message, error) {
	id, ok := m.Values["submission_id"].(string)
	if !ok || id == "" {
		return Message{}, errors.New("invalid queue message")
	}
	pending, err := q.client.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: q.name, Group: "judge-workers", Start: m.ID, End: m.ID, Count: 1}).Result()
	if err != nil || len(pending) != 1 {
		return Message{}, errors.New("pending delivery metadata unavailable")
	}
	return Message{ID: m.ID, SubmissionID: id, Deliveries: pending[0].RetryCount}, nil
}

// Ack confirms a result is durable; retained stream history is managed by operators.
func (q *Stream) Ack(ctx context.Context, id string) error {
	return q.client.XAck(ctx, q.name, "judge-workers", id).Err()
}

// DeadLetter atomically copies an exhausted delivery and acknowledges its source message.
func (q *Stream) DeadLetter(ctx context.Context, delivery judge.Delivery, reason string) error {
	if reason == "" {
		return errors.New("dead-letter reason required")
	}
	const script = `redis.call('XADD', KEYS[2], '*', 'submission_id', ARGV[2], 'source_message_id', ARGV[1], 'deliveries', ARGV[3], 'reason', ARGV[4]); return redis.call('XACK', KEYS[1], 'judge-workers', ARGV[1])`
	return q.client.Eval(ctx, script, []string{q.name, q.name + ":dead"}, delivery.ID, delivery.SubmissionID, delivery.Deliveries, reason).Err()
}

// Allow atomically increments a fixed-window counter and sets its expiry on first use.
func (q *Stream) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit < 1 || window < time.Millisecond {
		return false, errors.New("invalid rate limit")
	}
	count, e := q.client.Eval(ctx, `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end; return n`, []string{"judge:rate:" + key}, window.Milliseconds()).Int64()
	return count <= int64(limit), e
}
