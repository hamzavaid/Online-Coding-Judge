package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/submissions"
)

// Claim atomically reserves a queued submission; already active or terminal jobs are rejected.
func (s *Store) Claim(ctx context.Context, id string) (job judge.Job, err error) {
	var snapshot []byte
	err = s.Pool.QueryRow(ctx, "UPDATE submissions SET status='CLAIMED' WHERE id=$1 AND status='QUEUED' RETURNING id,language_id,source_code,problem_snapshot", id).Scan(&job.ID, &job.Language, &job.Source, &snapshot)
	if err == nil {
		err = json.Unmarshal(snapshot, &job.Problem)
	}
	return
}

// Transition conditionally writes an explicit legal state edge, preventing terminal mutation.
func (s *Store) Transition(ctx context.Context, id, to string) error {
	var from string
	if e := s.Pool.QueryRow(ctx, "SELECT status FROM submissions WHERE id=$1", id).Scan(&from); e != nil {
		return e
	}
	if !submissions.CanTransition(from, to) {
		return errors.New("invalid state transition")
	}
	tag, e := s.Pool.Exec(ctx, "UPDATE submissions SET status=$3 WHERE id=$1 AND status=$2", id, from, to)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return errors.New("state changed")
	}
	return nil
}

// Finish writes the entire aggregate in one conditional statement before queue acknowledgement.
func (s *Store) Finish(ctx context.Context, id string, r judge.Result) error {
	tag, e := s.Pool.Exec(ctx, "UPDATE submissions SET status='FINAL',verdict=$2,runtime_ms=$3,memory_kb=$4,tests_passed=$5,tests_total=$6,finished_at=now() WHERE id=$1 AND (status='RUNNING' OR (status='COMPILING' AND $2='COMPILATION_ERROR'))", id, r.Verdict, r.RuntimeMS, r.MemoryKB, r.TestsPassed, r.TestsTotal)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return errors.New("submission cannot finalize")
	}
	return nil
}

// Fail records infrastructure or queue publication failure without inventing a user verdict.
func (s *Store) Fail(ctx context.Context, id string) error {
	tag, e := s.Pool.Exec(ctx, "UPDATE submissions SET status='FAILED_INTERNAL',finished_at=now() WHERE id=$1 AND status IN ('QUEUED','CLAIMED','COMPILING','RUNNING')", id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return errors.New("submission cannot fail")
	}
	return nil
}
