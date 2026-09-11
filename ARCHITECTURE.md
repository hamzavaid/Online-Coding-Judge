# Architecture

The platform uses a modular Go API (Gin), PostgreSQL (pgx), and a React/Next.js client. A separate Go worker and Redis Streams provide asynchronous judging in the judge milestone. Submitted code never runs in the API process.

The API owns accounts, expiring bearer sessions, staff problem authoring, and submissions. Passwords use bcrypt; only token digests are stored. Public responses contain sample cases only. Submission status is owner-only and never includes hidden input or execution output.

Problem writes are transactional. Submission creation locks the problem and snapshots its limits and tests, so author edits cannot change queued work. Deletion disables a problem while preserving history.

The submission flow for the first three milestones is browser → API → PostgreSQL → Redis → single worker → Docker sandbox → PostgreSQL result → browser polling. Python and C++ are the initial languages. Expected outputs stay in the worker.

Queue recovery, multiple-worker coordination, Kubernetes, contests, and dashboards belong to later milestones.

Redis Streams carry submission IDs only. The single worker claims a queued row, compiles or syntax-checks the source, evaluates cases, writes the aggregate with a conditional PostgreSQL update, and acknowledges the delivery. Terminal states reject further transitions. Infrastructure failures are stored separately from user verdicts. A fixed launch marker starts execution deadlines after Docker startup; no submitted text is inserted into shell commands.

Compilation uses its own container and budget. The worker transfers only a bounded source or compiled artifact to a fresh container for each test. Host cgroup v2 counters supply memory and OOM evidence; writable sandbox files cannot supply verdict signals. Docker enforces network denial, read-only root, UID/capability restrictions, CPU/memory/PID limits, and bounded temporary filesystems. A synchronized output budget caps stdout and stderr together and cancels flooding execution.

Redis also owns expiring rate-limit counters for authentication and submissions. Public reads remain independent of Redis. The test workflow runs real PostgreSQL, Redis, Docker golden/security tests, Go race/vet checks, frontend tests/build, and dependency scans.
