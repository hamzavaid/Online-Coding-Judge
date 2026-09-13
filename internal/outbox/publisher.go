// Package outbox reliably transfers committed submission events to the work queue.
package outbox

import (
	"context"
	"time"
)

// Event is a leased database event whose ID remains private to the publisher.
type Event struct {
	ID           string
	SubmissionID string
}

// Store leases outbox rows and records successful publication.
type Store interface {
	ClaimOutbox(context.Context, string, int, time.Duration) ([]Event, error)
	MarkPublished(context.Context, string) error
}

// SubmissionPublisher sends an authoritative submission identifier to the queue.
type SubmissionPublisher interface {
	Publish(context.Context, string) error
}

// Publisher processes small leased batches; duplicate publication remains safe by design.
type Publisher struct {
	ID        string
	Store     Store
	Queue     SubmissionPublisher
	Lease     time.Duration
	BatchSize int
}

// PublishBatch publishes due rows and marks each one only after queue success.
func (p *Publisher) PublishBatch(ctx context.Context) (int, error) {
	lease := p.Lease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	size := p.BatchSize
	if size <= 0 {
		size = 50
	}
	events, err := p.Store.ClaimOutbox(ctx, p.ID, size, lease)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, event := range events {
		if err = p.Queue.Publish(ctx, event.SubmissionID); err != nil {
			return published, err
		}
		if err = p.Store.MarkPublished(ctx, event.ID); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}
