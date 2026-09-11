// Package queue implements the single-worker Redis Streams transport.
package queue

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
	"strings"
	"time"
)

// Message contains identifiers only; source and hidden tests remain in PostgreSQL.
type Message struct {
	ID           string
	SubmissionID string
}

// Stream uses one fixed consumer for the Phase 2 single-worker deployment.
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
func (q *Stream) Receive(ctx context.Context) (Message, error) {
	e := q.client.XGroupCreateMkStream(ctx, q.name, "judge-workers", "0").Err()
	if e != nil && !strings.HasPrefix(e.Error(), "BUSYGROUP") {
		return Message{}, e
	}
	streams, e := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "judge-workers", Consumer: "single-worker", Streams: []string{q.name, ">"}, Count: 1, Block: time.Second}).Result()
	if e != nil {
		return Message{}, e
	}
	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		return Message{}, redis.Nil
	}
	m := streams[0].Messages[0]
	id, ok := m.Values["submission_id"].(string)
	if !ok || id == "" {
		return Message{}, errors.New("invalid queue message")
	}
	return Message{ID: m.ID, SubmissionID: id}, nil
}

// Ack confirms a result is durable; retained stream history is managed by operators.
func (q *Stream) Ack(ctx context.Context, id string) error {
	return q.client.XAck(ctx, q.name, "judge-workers", id).Err()
}
