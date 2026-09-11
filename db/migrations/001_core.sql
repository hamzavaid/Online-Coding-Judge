BEGIN;
CREATE TABLE users (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), username text NOT NULL UNIQUE,
 email text NOT NULL UNIQUE, password_hash text NOT NULL,
 role text NOT NULL DEFAULT 'user' CHECK(role IN ('user','admin')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions (
 token_hash text PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at timestamptz NOT NULL
);
CREATE TABLE languages (id text PRIMARY KEY, name text NOT NULL);
INSERT INTO languages VALUES ('python','Python 3'),('cpp','C++23');
CREATE TABLE problems (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), slug text NOT NULL UNIQUE,
 title text NOT NULL, statement text NOT NULL, difficulty text NOT NULL,
 time_limit_ms integer NOT NULL CHECK(time_limit_ms BETWEEN 100 AND 10000),
 memory_limit_mb integer NOT NULL CHECK(memory_limit_mb BETWEEN 32 AND 512),
 status text NOT NULL CHECK(status IN ('draft','published','disabled'))
);
CREATE TABLE problem_languages (
 problem_id uuid NOT NULL REFERENCES problems(id), language_id text NOT NULL REFERENCES languages(id),
 PRIMARY KEY(problem_id,language_id)
);
CREATE TABLE test_cases (
 problem_id uuid NOT NULL REFERENCES problems(id), ordinal integer NOT NULL,
 input text NOT NULL, expected text NOT NULL, hidden boolean NOT NULL,
 PRIMARY KEY(problem_id,ordinal)
);
CREATE TABLE submissions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id),
 problem_id uuid NOT NULL REFERENCES problems(id), language_id text NOT NULL REFERENCES languages(id),
 source_code text NOT NULL CHECK(octet_length(source_code) BETWEEN 1 AND 65536),
 problem_snapshot jsonb NOT NULL,
 status text NOT NULL DEFAULT 'QUEUED' CHECK(status IN ('QUEUED','CLAIMED','COMPILING','RUNNING','FINAL','FAILED_INTERNAL')),
 verdict text CHECK(verdict IN ('ACCEPTED','WRONG_ANSWER','TIME_LIMIT_EXCEEDED','MEMORY_LIMIT_EXCEEDED','RUNTIME_ERROR','COMPILATION_ERROR','OUTPUT_LIMIT_EXCEEDED')),
 runtime_ms integer NOT NULL DEFAULT 0, memory_kb integer NOT NULL DEFAULT 0,
 tests_passed integer NOT NULL DEFAULT 0, tests_total integer NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz
);
CREATE INDEX submissions_user_created ON submissions(user_id,created_at DESC);
CREATE INDEX submissions_status_created ON submissions(status,created_at);
COMMIT;
