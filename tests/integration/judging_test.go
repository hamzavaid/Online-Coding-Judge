package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/api"
	"github.com/hamzavaid/Online-Coding-Judge/internal/auth"
	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/outbox"
	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
	"github.com/hamzavaid/Online-Coding-Judge/internal/queue"
	"github.com/redis/go-redis/v9"
)

// TestSubmissionToVerdict follows HTTP -> PostgreSQL -> Redis -> Docker -> owner-only result.
func TestSubmissionToVerdict(t *testing.T) {
	if os.Getenv("TEST_DOCKER") != "1" || os.Getenv("TEST_REDIS_ADDR") == "" {
		t.Skip("Docker and Redis integration settings required")
	}
	pool := testDB(t)
	store := database.New(pool)
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: os.Getenv("TEST_REDIS_ADDR")})
	defer client.Close()
	stream := fmt.Sprintf("test:e2e:%d", time.Now().UnixNano())
	defer client.Del(ctx, stream)
	q := queue.New(client, stream)
	h := api.New(store, q)
	var user string
	if e := pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash) VALUES('runner','runner@example.com','unused') RETURNING id").Scan(&user); e != nil {
		t.Fatal(e)
	}
	token, e := auth.Token()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO sessions VALUES($1,$2,now()+interval '1 hour')", auth.Digest(token), user); e != nil {
		t.Fatal(e)
	}
	p, e := store.SaveProblem(ctx, problems.Problem{Slug: "sum", Title: "Sum", Statement: "Add", Status: "published", Languages: []string{"python", "cpp"}, TimeLimitMS: 2000, MemoryLimitMB: 128, Tests: []problems.TestCase{{Input: "1 2", Expected: "3"}, {Input: "20 22", Expected: "42", Hidden: true}}})
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct{ lang, source string }{{"python", "print(sum(map(int,input().split())))"}, {"cpp", "#include <iostream>\nint main(){int a,b;std::cin>>a>>b;std::cout<<a+b;}"}} {
		body, _ := json.Marshal(map[string]string{"problem_id": p.ID, "language_id": c.lang, "source_code": c.source})
		r := httptest.NewRequest("POST", "/v1/submissions", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		var sub database.Submission
		if e = json.Unmarshal(w.Body.Bytes(), &sub); e != nil {
			t.Fatal(e)
		}
		publisher := outbox.Publisher{ID: "integration-publisher", Store: store, Queue: q, Lease: time.Minute, BatchSize: 10}
		if published, publishErr := publisher.PublishBatch(ctx); publishErr != nil || published != 1 {
			t.Fatalf("published=%d error=%v", published, publishErr)
		}
		msg, e := q.Receive(ctx, "worker-one", time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		worker := judge.Worker{ID: "worker-one", Lease: time.Minute, MaxDeliveries: 3, Repo: store, Queue: q, Engine: judge.Engine{Factory: judge.DockerFactory{Images: map[string]string{"python": os.Getenv("PYTHON_IMAGE"), "cpp": os.Getenv("CPP_IMAGE")}}}}
		if e = worker.Handle(ctx, msg); e != nil {
			t.Fatal(e)
		}
		r = httptest.NewRequest("GET", "/v1/submissions/"+sub.ID, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		if e = json.Unmarshal(w.Body.Bytes(), &sub); e != nil {
			t.Fatal(e)
		}
		if sub.Status != "FINAL" || sub.Verdict == nil || *sub.Verdict != "ACCEPTED" || sub.TestsPassed != 2 {
			t.Fatalf("%+v", sub)
		}
		var attemptID string
		if e = pool.QueryRow(ctx, "SELECT current_attempt_id FROM submissions WHERE id=$1", sub.ID).Scan(&attemptID); e != nil {
			t.Fatal(e)
		}
		if e = store.Transition(ctx, attemptID, "RUNNING"); e == nil {
			t.Fatal("terminal result mutable")
		}
	}
}
