package outbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	events []Event
	marked []string
}

func (s *fakeStore) ClaimOutbox(context.Context, string, int, time.Duration) ([]Event, error) {
	return s.events, nil
}
func (s *fakeStore) MarkPublished(_ context.Context, id string) error {
	s.marked = append(s.marked, id)
	return nil
}

type fakePublisher struct {
	published []string
	fail      bool
}

func (p *fakePublisher) Publish(_ context.Context, id string) error {
	if p.fail {
		return errors.New("redis unavailable")
	}
	p.published = append(p.published, id)
	return nil
}

// TestPublishBatch marks only events successfully handed to the at-least-once queue.
func TestPublishBatch(t *testing.T) {
	store := &fakeStore{events: []Event{{ID: "out-1", SubmissionID: "sub-1"}, {ID: "out-2", SubmissionID: "sub-2"}}}
	queue := &fakePublisher{}
	p := Publisher{ID: "publisher-a", Store: store, Queue: queue, Lease: time.Minute, BatchSize: 10}
	n, err := p.PublishBatch(context.Background())
	if err != nil || n != 2 || !equal(queue.published, []string{"sub-1", "sub-2"}) || !equal(store.marked, []string{"out-1", "out-2"}) {
		t.Fatalf("n=%d err=%v published=%v marked=%v", n, err, queue.published, store.marked)
	}
	queue.fail = true
	store.marked = nil
	if _, err = p.PublishBatch(context.Background()); err == nil || len(store.marked) != 0 {
		t.Fatalf("failed publication marked: %v", store.marked)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
