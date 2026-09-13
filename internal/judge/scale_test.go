package judge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
)

type scaleRepo struct {
	events     *[]string
	claim      error
	attempt    int
	deadLetter bool
	renewed    *atomic.Int32
}

func (r scaleRepo) Claim(context.Context, string, string, time.Duration) (Job, error) {
	*r.events = append(*r.events, "claim")
	if r.claim != nil {
		return Job{DeadLetter: r.deadLetter}, r.claim
	}
	return Job{ID: "sub", AttemptID: "attempt", AttemptNumber: r.attempt, Language: "python"}, nil
}
func (r scaleRepo) Transition(context.Context, string, string) error { return nil }
func (r scaleRepo) Finish(context.Context, string, Result) error     { return nil }
func (r scaleRepo) Fail(context.Context, string) error {
	*r.events = append(*r.events, "fail")
	return nil
}
func (r scaleRepo) Retry(context.Context, string, time.Duration) error {
	*r.events = append(*r.events, "retry")
	return nil
}
func (r scaleRepo) Renew(context.Context, string, time.Duration) error {
	if r.renewed != nil {
		r.renewed.Add(1)
	}
	return nil
}

type scaleQueue struct{ events *[]string }

func (q scaleQueue) Ack(context.Context, string) error {
	*q.events = append(*q.events, "ack")
	return nil
}
func (q scaleQueue) DeadLetter(context.Context, Delivery, string) error {
	*q.events = append(*q.events, "dlq")
	return nil
}

type brokenFactory struct{}

func (brokenFactory) Open(context.Context, Job) (Sandbox, error) {
	return nil, errors.New("transient infrastructure failure")
}

// TestWorkerDeliveryPolicy verifies terminal idempotency and bounded infrastructure retries.
func TestWorkerDeliveryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		claim      error
		deliveries int64
		deadLetter bool
		want       []string
		wantError  bool
	}{
		{"terminal duplicate", ErrTerminal, 2, false, []string{"claim", "ack"}, false},
		{"terminal exhausted retry", ErrTerminal, 3, true, []string{"claim", "dlq"}, false},
		{"transient retry", nil, 2, false, []string{"claim", "retry", "ack"}, false},
		{"exhausted to dlq", nil, 3, false, []string{"claim", "fail", "dlq"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []string{}
			w := Worker{ID: "worker-a", Lease: time.Minute, MaxDeliveries: 3, Repo: scaleRepo{events: &events, claim: tc.claim, attempt: int(tc.deliveries), deadLetter: tc.deadLetter}, Queue: scaleQueue{&events}, Engine: Engine{Factory: brokenFactory{}}}
			err := w.Handle(context.Background(), Delivery{ID: "message", SubmissionID: "sub", Deliveries: tc.deliveries})
			if !equalStrings(events, tc.want) || (err != nil) != tc.wantError {
				t.Fatalf("events=%v error=%v", events, err)
			}
		})
	}
}

type slowFactory struct{}

func (slowFactory) Open(context.Context, Job) (Sandbox, error) { return &slowSandbox{}, nil }

type slowSandbox struct{}

func (*slowSandbox) Compile(context.Context) (Outcome, error) { return Outcome{}, nil }
func (*slowSandbox) Run(ctx context.Context, _ string) (Outcome, error) {
	select {
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	case <-time.After(45 * time.Millisecond):
		return Outcome{Output: "ok"}, nil
	}
}
func (*slowSandbox) Close() error { return nil }

// TestWorkerRenewsLease keeps a long execution fenced while it remains healthy.
func TestWorkerRenewsLease(t *testing.T) {
	events := []string{}
	var renewed atomic.Int32
	repo := scaleRepo{events: &events, attempt: 1, renewed: &renewed}
	// Supply test data through a small wrapper while retaining renewal counters.
	r := renewRepo{scaleRepo: repo}
	w := Worker{ID: "worker", Lease: 30 * time.Millisecond, Repo: r, Queue: scaleQueue{&events}, Engine: Engine{Factory: slowFactory{}}}
	err := w.Handle(context.Background(), Delivery{ID: "message", SubmissionID: "sub"})
	if err != nil || renewed.Load() < 2 {
		t.Fatalf("error=%v renewals=%d", err, renewed.Load())
	}
}

type renewRepo struct{ scaleRepo }

func (r renewRepo) Claim(ctx context.Context, id, worker string, lease time.Duration) (Job, error) {
	job, e := r.scaleRepo.Claim(ctx, id, worker, lease)
	job.Problem.Tests = []problems.TestCase{{Expected: "ok"}}
	return job, e
}

// TestRunPoolBoundsConcurrency proves the worker pool drains jobs without exceeding its configured width.
func TestRunPoolBoundsConcurrency(t *testing.T) {
	jobs := make(chan Delivery, 20)
	for i := 0; i < cap(jobs); i++ {
		jobs <- Delivery{ID: "message"}
	}
	close(jobs)
	var active, peak, completed atomic.Int32
	err := RunPool(context.Background(), 3, func(ctx context.Context, _ int) error {
		for range jobs {
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			completed.Add(1)
		}
		return nil
	})
	if err != nil || completed.Load() != 20 || peak.Load() > 3 || peak.Load() < 2 {
		t.Fatalf("error=%v completed=%d peak=%d", err, completed.Load(), peak.Load())
	}
}

func equalStrings(a, b []string) bool {
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
