package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/api"
	"github.com/hamzavaid/Online-Coding-Judge/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testDB gives each test an isolated schema and applies the production migration.
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); root.Close() })
	sql, err := os.ReadFile("../../db/migrations/001_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	return pool
}

// TestPlatform exercises real persistence, authentication, RBAC, and hidden-data boundaries.
func TestPlatform(t *testing.T) {
	pool := testDB(t)
	h := api.New(database.New(pool))
	ctx := context.Background()
	request := func(method, path, body, token string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var v map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return v
	}
	register := `{"username":"alice","email":"alice@example.com","password":"correct horse battery staple"}`
	request("POST", "/v1/auth/register", `{"username":"admin","email":"x@x.com","password":"long password here","role":"admin"}`, "", 400)
	request("POST", "/v1/auth/register", register, "", 201)
	request("POST", "/v1/auth/register", register, "", 409)
	request("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"wrong"}`, "", 401)
	token := request("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"correct horse battery staple"}`, "", 200)["token"].(string)
	request("GET", "/v1/users/me", "", "", 401)
	request("GET", "/v1/users/me", "", token, 200)
	problem := `{"slug":"sum","title":"Sum","statement":"Add two integers","difficulty":"easy","time_limit_ms":1000,"memory_limit_mb":128,"status":"published","languages":["python","cpp"],"tests":[{"input":"1 2","expected":"3","hidden":false},{"input":"secret input","expected":"secret expected","hidden":true}]}`
	request("POST", "/v1/admin/problems", problem, token, 403)
	if _, err := pool.Exec(ctx, "UPDATE users SET role='admin' WHERE username='alice'"); err != nil {
		t.Fatal(err)
	}
	id := request("POST", "/v1/admin/problems", problem, token, 201)["id"].(string)
	public := request("GET", "/v1/problems/"+id, "", "", 200)
	b, _ := json.Marshal(public)
	if strings.Contains(string(b), "secret") {
		t.Fatal("hidden test leak")
	}
	request("GET", "/v1/problems", "", "", 200)
	request("POST", "/v1/submissions", `{"problem_id":"`+id+`","language_id":"shell","source_code":"hi"}`, token, 400)
	sub := request("POST", "/v1/submissions", `{"problem_id":"`+id+`","language_id":"python","source_code":"print(sum(map(int,input().split())))"}`, token, 202)["submission_id"].(string)
	request("GET", "/v1/submissions/"+sub, "", token, 200)
	request("GET", "/v1/users/me/submissions", "", token, 200)
	request("POST", "/v1/auth/register", `{"username":"bob","email":"bob@example.com","password":"another long password"}`, "", 201)
	bob := request("POST", "/v1/auth/login", `{"email":"bob@example.com","password":"another long password"}`, "", 200)["token"].(string)
	request("GET", "/v1/submissions/"+sub, "", bob, 404)
	request("GET", "/v1/admin/problems/"+id, "", bob, 403)
	request("GET", "/v1/admin/problems/"+id, "", token, 200)
	request("PUT", "/v1/admin/problems/"+id, strings.Replace(problem, "Add two integers", "Compute the sum", 1), token, 200)
	request("DELETE", "/v1/admin/problems/"+id, "", token, 204)
	request("GET", "/v1/problems/"+id, "", "", 404)
	request("POST", "/v1/submissions", `{"problem_id":"`+id+`","language_id":"python","source_code":"print(3)"}`, token, 400)
	if _, err := pool.Exec(ctx, "UPDATE sessions SET expires_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	request("GET", "/v1/users/me", "", token, 401)
	_ = http.MethodGet
}
