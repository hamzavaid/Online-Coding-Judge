package integration

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/api"
	"github.com/hamzavaid/Online-Coding-Judge/internal/auth"
	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
)

func seededSubmission(t *testing.T, store *database.Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	var user string
	if err := store.Pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash) VALUES('scale','scale@example.com','unused') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	p, err := store.SaveProblem(ctx, problems.Problem{Slug: "scale", Title: "Scale", Statement: "Scale", Status: "published", Languages: []string{"python"}, TimeLimitMS: 1000, MemoryLimitMB: 64, Tests: []problems.TestCase{{Expected: "ok"}}})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubmission(ctx, user, p.ID, "python", "print('ok')")
	if err != nil {
		t.Fatal(err)
	}
	return user, sub.ID
}

// TestSubmissionOutboxAtomicity verifies every committed submission has exactly one unsent event.
func TestSubmissionOutboxAtomicity(t *testing.T) {
	store := database.New(testDB(t))
	_, id := seededSubmission(t, store)
	var count int
	if err := store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM outbox_events WHERE submission_id=$1 AND published_at IS NULL", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

// TestConcurrentLeasedClaims permits one active attempt, then fences it after lease expiry.
func TestConcurrentLeasedClaims(t *testing.T) {
	store := database.New(testDB(t))
	_, id := seededSubmission(t, store)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan struct {
		job judge.Job
		err error
	}, 2)
	var wg sync.WaitGroup
	for _, worker := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(worker string) {
			defer wg.Done()
			<-start
			job, err := store.Claim(ctx, id, worker, 300*time.Millisecond)
			results <- struct {
				job judge.Job
				err error
			}{job, err}
		}(worker)
	}
	close(start)
	wg.Wait()
	close(results)
	var first judge.Job
	wins, busy := 0, 0
	for result := range results {
		if result.err == nil {
			wins++
			first = result.job
		} else if errors.Is(result.err, judge.ErrBusy) {
			busy++
		} else {
			t.Fatal(result.err)
		}
	}
	if wins != 1 || busy != 1 {
		t.Fatalf("wins=%d busy=%d", wins, busy)
	}
	time.Sleep(350 * time.Millisecond)
	second, err := store.Claim(ctx, id, "worker-c", time.Minute)
	if err != nil || second.AttemptID == first.AttemptID {
		t.Fatalf("reclaim=%+v err=%v", second, err)
	}
	var expiredStatus, expiredCode string
	if err = store.Pool.QueryRow(ctx, "SELECT status,coalesce(error_code,'') FROM submission_attempts WHERE id=$1", first.AttemptID).Scan(&expiredStatus, &expiredCode); err != nil || expiredStatus != "FAILED_INTERNAL" || expiredCode != "LEASE_EXPIRED" {
		t.Fatalf("expired attempt status=%q code=%q err=%v", expiredStatus, expiredCode, err)
	}
	if err = store.Transition(ctx, first.AttemptID, "RUNNING"); !errors.Is(err, judge.ErrStaleAttempt) {
		t.Fatalf("stale transition: %v", err)
	}
	if err = store.Transition(ctx, second.AttemptID, "COMPILING"); err != nil {
		t.Fatal(err)
	}
	if err = store.Transition(ctx, second.AttemptID, "RUNNING"); err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(ctx, second.AttemptID, judge.Result{Verdict: "ACCEPTED", TestsPassed: 1, TestsTotal: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Claim(ctx, id, "worker-d", time.Minute); !errors.Is(err, judge.ErrTerminal) {
		t.Fatalf("terminal claim: %v", err)
	}
	var attempts int
	if err = store.Pool.QueryRow(ctx, "SELECT count(*) FROM submission_attempts WHERE submission_id=$1", id).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

// TestOutboxLeasesPartitionWork verifies simultaneous publishers cannot claim the same event.
func TestOutboxLeasesPartitionWork(t *testing.T) {
	store := database.New(testDB(t))
	seededSubmission(t, store)
	ctx := context.Background()
	var a, b []database.OutboxEvent
	var ea, eb error
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; a, ea = store.ClaimOutbox(ctx, "publisher-a", 10, time.Minute) }()
	go func() { defer wg.Done(); <-start; b, eb = store.ClaimOutbox(ctx, "publisher-b", 10, time.Minute) }()
	close(start)
	wg.Wait()
	if ea != nil || eb != nil || len(a)+len(b) != 1 {
		t.Fatalf("a=%v b=%v errors=%v/%v", a, b, ea, eb)
	}
}

// TestReconcileDoesNotRepublishDeliveredSubmission prevents the reconciler from
// creating an endless stream of duplicate jobs while a published job is queued.
func TestReconcileDoesNotRepublishDeliveredSubmission(t *testing.T) {
	store := database.New(testDB(t))
	_, id := seededSubmission(t, store)
	ctx := context.Background()
	events, err := store.ClaimOutbox(ctx, "publisher", 1, time.Minute)
	if err != nil || len(events) != 1 {
		t.Fatalf("claim=%v err=%v", events, err)
	}
	if err = store.MarkPublished(ctx, events[0].ID); err != nil {
		t.Fatal(err)
	}
	created, err := store.ReconcileOutbox(ctx)
	if err != nil || created != 0 {
		t.Fatalf("reconciled=%d err=%v", created, err)
	}
	var count int
	if err = store.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE submission_id=$1", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("events=%d err=%v", count, err)
	}
}

// TestReconcileRepublishesStaleQueuedSubmission repairs a job that remained
// queued long after its last publication without creating immediate repeats.
func TestReconcileRepublishesStaleQueuedSubmission(t *testing.T) {
	store := database.New(testDB(t))
	_, id := seededSubmission(t, store)
	ctx := context.Background()
	events, err := store.ClaimOutbox(ctx, "publisher", 1, time.Minute)
	if err != nil || len(events) != 1 {
		t.Fatalf("claim=%v err=%v", events, err)
	}
	if err = store.MarkPublished(ctx, events[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, "UPDATE outbox_events SET created_at=now()-interval '6 minutes' WHERE submission_id=$1", id); err != nil {
		t.Fatal(err)
	}
	created, err := store.ReconcileOutbox(ctx)
	if err != nil || created != 1 {
		t.Fatalf("first reconcile=%d err=%v", created, err)
	}
	created, err = store.ReconcileOutbox(ctx)
	if err != nil || created != 0 {
		t.Fatalf("second reconcile=%d err=%v", created, err)
	}
}

// TestSubmissionEventsAuthenticatesOwnerAndEndsAtTerminal validates the SSE privacy boundary.
func TestSubmissionEventsAuthenticatesOwnerAndEndsAtTerminal(t *testing.T) {
	store := database.New(testDB(t))
	user, id := seededSubmission(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, "UPDATE submissions SET status='FINAL',verdict='ACCEPTED',finished_at=now() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.Token()
	if _, err := store.Pool.Exec(ctx, "INSERT INTO sessions VALUES($1,$2,now()+interval '1 hour')", auth.Digest(token), user); err != nil {
		t.Fatal(err)
	}
	h := api.New(store)
	r := httptest.NewRequest("GET", "/v1/submissions/"+id+"/events", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(w.Body.String(), "event: submission") || !strings.Contains(w.Body.String(), `"status":"FINAL"`) || strings.Contains(w.Body.String(), "source_code") {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	var other string
	if err := store.Pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash) VALUES('other','other@example.com','unused') RETURNING id").Scan(&other); err != nil {
		t.Fatal(err)
	}
	otherToken, _ := auth.Token()
	_, _ = store.Pool.Exec(ctx, "INSERT INTO sessions VALUES($1,$2,now()+interval '1 hour')", auth.Digest(otherToken), other)
	r = httptest.NewRequest("GET", "/v1/submissions/"+id+"/events", nil)
	r.Header.Set("Authorization", "Bearer "+otherToken)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("cross-user SSE status=%d", w.Code)
	}
}
