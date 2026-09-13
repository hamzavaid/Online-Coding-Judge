BEGIN;

ALTER TABLE submissions
  ADD COLUMN current_attempt_id uuid,
  ADD COLUMN lease_until timestamptz,
  ADD COLUMN retry_after timestamptz,
  ADD COLUMN attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0);

CREATE TABLE submission_attempts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  submission_id uuid NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
  attempt_number integer NOT NULL CHECK (attempt_number > 0),
  worker_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('CLAIMED','COMPILING','RUNNING','FINAL','FAILED_INTERNAL')),
  error_code text,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  UNIQUE (submission_id, attempt_number)
);

ALTER TABLE submissions
  ADD CONSTRAINT submissions_current_attempt_fk
  FOREIGN KEY (current_attempt_id) REFERENCES submission_attempts(id);

CREATE TABLE outbox_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  submission_id uuid NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
  available_at timestamptz NOT NULL DEFAULT now(),
  claimed_by text,
  claimed_until timestamptz,
  published_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX outbox_ready
  ON outbox_events(available_at, created_at)
  WHERE published_at IS NULL;
CREATE INDEX attempts_submission
  ON submission_attempts(submission_id, attempt_number DESC);

COMMIT;
