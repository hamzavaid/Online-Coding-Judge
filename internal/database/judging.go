package database

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/outbox"
	"github.com/hamzavaid/Online-Coding-Judge/internal/submissions"
	"github.com/jackc/pgx/v5"
)

// Claim creates a fenced judging attempt when no live lease or retry delay blocks it.
func (s *Store) Claim(ctx context.Context, id, worker string, lease time.Duration) (job judge.Job, err error) {
	if worker == "" || lease <= 0 {
		return job, errors.New("invalid worker lease")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return job, err
	}
	defer tx.Rollback(ctx)
	var status string
	var currentAttempt *string
	var errorCode string
	var leaseUntil, retryAfter *time.Time
	var snapshot []byte
	err = tx.QueryRow(ctx, `SELECT s.status,s.current_attempt_id,s.lease_until,s.retry_after,s.language_id,s.source_code,s.problem_snapshot,s.attempt_count,
		COALESCE((SELECT a.error_code FROM submission_attempts a WHERE a.id=s.current_attempt_id),'')
		FROM submissions s WHERE s.id=$1 FOR UPDATE`, id).Scan(&status, &currentAttempt, &leaseUntil, &retryAfter, &job.Language, &job.Source, &snapshot, &job.AttemptNumber, &errorCode)
	if err != nil {
		return job, err
	}
	now := time.Now()
	if status == "FINAL" || status == "FAILED_INTERNAL" {
		job.DeadLetter = errorCode == "ATTEMPTS_EXHAUSTED"
		return job, judge.ErrTerminal
	}
	if retryAfter != nil && retryAfter.After(now) {
		return job, judge.ErrBusy
	}
	if status != "QUEUED" && leaseUntil != nil && leaseUntil.After(now) {
		return job, judge.ErrBusy
	}
	if status != "QUEUED" && currentAttempt != nil {
		if _, err = tx.Exec(ctx, `UPDATE submission_attempts SET status='FAILED_INTERNAL',error_code='LEASE_EXPIRED',finished_at=now()
			WHERE id=$1 AND status IN ('CLAIMED','COMPILING','RUNNING')`, *currentAttempt); err != nil {
			return job, err
		}
	}
	job.ID = id
	job.AttemptNumber++
	err = tx.QueryRow(ctx, `INSERT INTO submission_attempts(submission_id,attempt_number,worker_id,status)
		VALUES($1,$2,$3,'CLAIMED') RETURNING id`, id, job.AttemptNumber, worker).Scan(&job.AttemptID)
	if err != nil {
		return job, err
	}
	_, err = tx.Exec(ctx, `UPDATE submissions SET status='CLAIMED',current_attempt_id=$2,lease_until=now()+$3::interval,
		retry_after=NULL,attempt_count=$4 WHERE id=$1`, id, job.AttemptID, lease.String(), job.AttemptNumber)
	if err != nil {
		return job, err
	}
	if err = json.Unmarshal(snapshot, &job.Problem); err != nil {
		return job, err
	}
	return job, tx.Commit(ctx)
}

// Transition writes attempt and submission state only while the attempt owns the fence.
func (s *Store) Transition(ctx context.Context, attemptID, to string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var submissionID, current, status string
	err = tx.QueryRow(ctx, `SELECT a.submission_id,s.current_attempt_id,a.status FROM submission_attempts a
		JOIN submissions s ON s.id=a.submission_id WHERE a.id=$1 FOR UPDATE OF a,s`, attemptID).Scan(&submissionID, &current, &status)
	if err != nil {
		return err
	}
	if current != attemptID {
		return judge.ErrStaleAttempt
	}
	if !submissions.CanTransition(status, to) {
		return errors.New("invalid state transition")
	}
	if _, err = tx.Exec(ctx, "UPDATE submission_attempts SET status=$2 WHERE id=$1", attemptID, to); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE submissions SET status=$2 WHERE id=$1 AND current_attempt_id=$3", submissionID, to, attemptID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Renew extends only the current attempt's lease, so a stale worker cannot regain ownership.
func (s *Store) Renew(ctx context.Context, attemptID string, lease time.Duration) error {
	if lease <= 0 {
		return errors.New("invalid worker lease")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE submissions SET lease_until=now()+$2::interval
		WHERE current_attempt_id=$1 AND status IN ('CLAIMED','COMPILING','RUNNING')`, attemptID, lease.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return judge.ErrStaleAttempt
	}
	return nil
}

// Finish atomically makes one attempt and its authoritative submission terminal.
func (s *Store) Finish(ctx context.Context, attemptID string, r judge.Result) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var submissionID, current, status string
	if err = tx.QueryRow(ctx, `SELECT a.submission_id,s.current_attempt_id,a.status FROM submission_attempts a
		JOIN submissions s ON s.id=a.submission_id WHERE a.id=$1 FOR UPDATE OF a,s`, attemptID).Scan(&submissionID, &current, &status); err != nil {
		return err
	}
	if current != attemptID {
		return judge.ErrStaleAttempt
	}
	if status != "RUNNING" && !(status == "COMPILING" && r.Verdict == "COMPILATION_ERROR") {
		return errors.New("attempt cannot finalize")
	}
	if _, err = tx.Exec(ctx, "UPDATE submission_attempts SET status='FINAL',finished_at=now() WHERE id=$1", attemptID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE submissions SET status='FINAL',verdict=$2,runtime_ms=$3,memory_kb=$4,
		tests_passed=$5,tests_total=$6,finished_at=now(),lease_until=NULL,retry_after=NULL WHERE id=$1 AND current_attempt_id=$7`, submissionID, r.Verdict, r.RuntimeMS, r.MemoryKB, r.TestsPassed, r.TestsTotal, attemptID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return judge.ErrStaleAttempt
	}
	return tx.Commit(ctx)
}

// Retry records the failed attempt and schedules durable delayed publication atomically.
func (s *Store) Retry(ctx context.Context, attemptID string, delay time.Duration) error {
	if delay < 0 || delay > 5*time.Minute {
		return errors.New("invalid retry delay")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var submissionID, current string
	if err = tx.QueryRow(ctx, `SELECT a.submission_id,s.current_attempt_id FROM submission_attempts a
		JOIN submissions s ON s.id=a.submission_id WHERE a.id=$1 FOR UPDATE OF a,s`, attemptID).Scan(&submissionID, &current); err != nil {
		return err
	}
	if current != attemptID {
		return judge.ErrStaleAttempt
	}
	if _, err = tx.Exec(ctx, "UPDATE submission_attempts SET status='FAILED_INTERNAL',error_code='TRANSIENT',finished_at=now() WHERE id=$1", attemptID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE submissions SET status='QUEUED',current_attempt_id=NULL,lease_until=NULL,retry_after=now()+$2::interval WHERE id=$1", submissionID, delay.String()); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO outbox_events(submission_id,available_at) VALUES($1,now()+$2::interval)", submissionID, delay.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Fail records exhausted infrastructure attempts without assigning a user verdict.
func (s *Store) Fail(ctx context.Context, attemptID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var submissionID, current string
	if err = tx.QueryRow(ctx, `SELECT a.submission_id,s.current_attempt_id FROM submission_attempts a
		JOIN submissions s ON s.id=a.submission_id WHERE a.id=$1 FOR UPDATE OF a,s`, attemptID).Scan(&submissionID, &current); err != nil {
		return err
	}
	if current != attemptID {
		return judge.ErrStaleAttempt
	}
	if _, err = tx.Exec(ctx, "UPDATE submission_attempts SET status='FAILED_INTERNAL',error_code='ATTEMPTS_EXHAUSTED',finished_at=now() WHERE id=$1", attemptID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE submissions SET status='FAILED_INTERNAL',finished_at=now(),lease_until=NULL,retry_after=NULL WHERE id=$1 AND current_attempt_id=$2", submissionID, attemptID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OutboxEvent identifies one durable submission publication request.
type OutboxEvent = outbox.Event

// ClaimOutbox leases ready rows with SKIP LOCKED so publishers partition work.
func (s *Store) ClaimOutbox(ctx context.Context, publisher string, limit int, lease time.Duration) ([]OutboxEvent, error) {
	if publisher == "" || limit < 1 || limit > 100 || lease <= 0 {
		return nil, errors.New("invalid outbox lease")
	}
	rows, err := s.Pool.Query(ctx, `WITH ready AS (SELECT id FROM outbox_events WHERE published_at IS NULL AND available_at<=now()
		AND (claimed_until IS NULL OR claimed_until<now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $1)
		UPDATE outbox_events o SET claimed_by=$2,claimed_until=now()+$3::interval FROM ready WHERE o.id=ready.id RETURNING o.id,o.submission_id`, limit, publisher, lease.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []OutboxEvent{}
	for rows.Next() {
		var event OutboxEvent
		if err = rows.Scan(&event.ID, &event.SubmissionID); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// MarkPublished closes an outbox lease after Redis accepts the event.
func (s *Store) MarkPublished(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, "UPDATE outbox_events SET published_at=now(),claimed_until=NULL WHERE id=$1 AND published_at IS NULL", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

// ReconcileOutbox restores missing publication work for queued submissions.
func (s *Store) ReconcileOutbox(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO outbox_events(submission_id)
		SELECT s.id FROM submissions s WHERE s.status='QUEUED' AND (s.retry_after IS NULL OR s.retry_after<=now())
		AND NOT EXISTS(SELECT 1 FROM outbox_events o WHERE o.submission_id=s.id AND o.published_at IS NULL)
		AND NOT EXISTS(SELECT 1 FROM outbox_events o WHERE o.submission_id=s.id AND o.created_at>=now()-interval '5 minutes')`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
