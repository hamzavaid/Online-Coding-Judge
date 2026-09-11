# Architecture

The platform uses a modular Go API (Gin), PostgreSQL (pgx), and a React/Next.js client. A separate Go worker and Redis Streams provide asynchronous judging in the judge milestone. Submitted code never runs in the API process.

The API owns accounts, expiring bearer sessions, staff problem authoring, and submissions. Passwords use bcrypt; only token digests are stored. Public responses contain sample cases only. Submission status is owner-only and never includes hidden input or execution output.

Problem writes are transactional. Submission creation locks the problem and snapshots its limits and tests, so author edits cannot change queued work. Deletion disables a problem while preserving history.

The submission flow for the first three milestones is browser → API → PostgreSQL → Redis → single worker → Docker sandbox → PostgreSQL result → browser polling. Python and C++ are the initial languages. Expected outputs stay in the worker.

Queue recovery, multiple-worker coordination, Kubernetes, contests, and dashboards belong to later milestones.

Redis Streams carry submission IDs only. The single worker claims a queued row, compiles or syntax-checks the source, evaluates cases, writes the aggregate with a conditional PostgreSQL update, and acknowledges the delivery. Terminal states reject further transitions. Infrastructure failures are stored separately from user verdicts. A fixed launch marker starts execution deadlines after Docker startup; no submitted text is inserted into shell commands.
