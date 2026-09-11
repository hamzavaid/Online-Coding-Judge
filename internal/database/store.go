// Package database implements PostgreSQL persistence with explicit transaction boundaries.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store owns a concurrency-safe PostgreSQL connection pool.
type Store struct{ Pool *pgxpool.Pool }

// New binds a repository to an already configured pool.
func New(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

// User is the public profile; password hashes are never serialized.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

// Submission exposes aggregate status without source, hidden inputs, or program output.
type Submission struct {
	ID          string    `json:"submission_id"`
	ProblemID   string    `json:"problem_id"`
	Language    string    `json:"language_id"`
	Status      string    `json:"status"`
	Verdict     *string   `json:"verdict"`
	RuntimeMS   int       `json:"runtime_ms"`
	MemoryKB    int       `json:"memory_kb"`
	TestsPassed int       `json:"tests_passed"`
	TestsTotal  int       `json:"tests_total"`
	CreatedAt   time.Time `json:"created_at"`
}

// Problem loads test data for trusted callers; APIs must apply Public before responding.
func (s *Store) Problem(ctx context.Context, id string) (problems.Problem, error) {
	return loadProblem(ctx, s.Pool, id)
}

// queryer permits the same loader to run inside a locking transaction.
type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// loadProblem reconstructs normalized rows using a consistent caller-owned transaction when needed.
func loadProblem(ctx context.Context, q queryer, id string) (p problems.Problem, err error) {
	err = q.QueryRow(ctx, "SELECT id,slug,title,statement,difficulty,time_limit_ms,memory_limit_mb,status FROM problems WHERE id=$1", id).Scan(&p.ID, &p.Slug, &p.Title, &p.Statement, &p.Difficulty, &p.TimeLimitMS, &p.MemoryLimitMB, &p.Status)
	if err != nil {
		return
	}
	rows, e := q.Query(ctx, "SELECT language_id FROM problem_languages WHERE problem_id=$1 ORDER BY language_id", id)
	if e != nil {
		return p, e
	}
	for rows.Next() {
		var l string
		if e = rows.Scan(&l); e != nil {
			rows.Close()
			return p, e
		}
		p.Languages = append(p.Languages, l)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	rows, e = q.Query(ctx, "SELECT input,expected,hidden FROM test_cases WHERE problem_id=$1 ORDER BY ordinal", id)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		var c problems.TestCase
		if e = rows.Scan(&c.Input, &c.Expected, &c.Hidden); e != nil {
			return p, e
		}
		p.Tests = append(p.Tests, c)
	}
	return p, rows.Err()
}

// SaveProblem atomically replaces authored metadata and tests; submissions retain their snapshot.
func (s *Store) SaveProblem(ctx context.Context, p problems.Problem) (problems.Problem, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	if p.ID == "" {
		e = tx.QueryRow(ctx, "INSERT INTO problems(slug,title,statement,difficulty,time_limit_ms,memory_limit_mb,status) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id", p.Slug, p.Title, p.Statement, p.Difficulty, p.TimeLimitMS, p.MemoryLimitMB, p.Status).Scan(&p.ID)
	} else {
		tag, err := tx.Exec(ctx, "UPDATE problems SET slug=$2,title=$3,statement=$4,difficulty=$5,time_limit_ms=$6,memory_limit_mb=$7,status=$8 WHERE id=$1", p.ID, p.Slug, p.Title, p.Statement, p.Difficulty, p.TimeLimitMS, p.MemoryLimitMB, p.Status)
		e = err
		if e == nil && tag.RowsAffected() != 1 {
			e = pgx.ErrNoRows
		}
	}
	if e != nil {
		return p, e
	}
	for _, table := range []string{"test_cases", "problem_languages"} {
		if _, e = tx.Exec(ctx, "DELETE FROM "+table+" WHERE problem_id=$1", p.ID); e != nil {
			return p, e
		}
	}
	for _, l := range p.Languages {
		if _, e = tx.Exec(ctx, "INSERT INTO problem_languages VALUES($1,$2)", p.ID, l); e != nil {
			return p, e
		}
	}
	for i, c := range p.Tests {
		if _, e = tx.Exec(ctx, "INSERT INTO test_cases VALUES($1,$2,$3,$4,$5)", p.ID, i, c.Input, c.Expected, c.Hidden); e != nil {
			return p, e
		}
	}
	return p, tx.Commit(ctx)
}

// CreateSubmission locks the problem while validating and freezing its execution data.
func (s *Store) CreateSubmission(ctx context.Context, user, id, language, source string) (Submission, error) {
	var sub Submission
	if len(source) < 1 || len(source) > 65536 {
		return sub, errors.New("invalid source")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return sub, e
	}
	defer tx.Rollback(ctx)
	var lock string
	if e = tx.QueryRow(ctx, "SELECT id FROM problems WHERE id=$1 FOR SHARE", id).Scan(&lock); e != nil {
		return sub, e
	}
	p, e := loadProblem(ctx, tx, id)
	if e != nil {
		return sub, e
	}
	valid := false
	for _, l := range p.Languages {
		valid = valid || l == language
	}
	if p.Status != "published" || !valid {
		return sub, errors.New("problem or language unavailable")
	}
	snapshot, e := json.Marshal(p)
	if e != nil {
		return sub, e
	}
	e = tx.QueryRow(ctx, "INSERT INTO submissions(user_id,problem_id,language_id,source_code,problem_snapshot,tests_total) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at", user, id, language, source, snapshot, len(p.Tests)).Scan(&sub.ID, &sub.CreatedAt)
	if e != nil {
		return sub, e
	}
	sub.Status = "QUEUED"
	sub.ProblemID = id
	sub.Language = language
	sub.TestsTotal = len(p.Tests)
	return sub, tx.Commit(ctx)
}

// History returns only an owner's bounded history, optionally filtered to a submission ID.
func (s *Store) History(ctx context.Context, user, id string) ([]Submission, error) {
	rows, e := s.Pool.Query(ctx, "SELECT id,problem_id,language_id,status,verdict,runtime_ms,memory_kb,tests_passed,tests_total,created_at FROM submissions WHERE user_id=$1 AND ($2='' OR id::text=$2) ORDER BY created_at DESC LIMIT 100", user, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Submission{}
	for rows.Next() {
		var sub Submission
		if e = rows.Scan(&sub.ID, &sub.ProblemID, &sub.Language, &sub.Status, &sub.Verdict, &sub.RuntimeMS, &sub.MemoryKB, &sub.TestsPassed, &sub.TestsTotal, &sub.CreatedAt); e != nil {
			return nil, e
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}
