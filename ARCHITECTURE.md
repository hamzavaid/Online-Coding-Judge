# Architecture

The platform uses a modular Go API (Gin), PostgreSQL (pgx), Redis Streams, and a React/Next.js client. Separate Go worker processes provide asynchronous judging. Submitted code never runs in the API process.

The API owns accounts, expiring bearer sessions, staff problem authoring, and submissions. Passwords use bcrypt; only token digests are stored. Public responses contain sample cases only. Submission status is owner-only and never includes hidden input or execution output.

Problem writes are transactional. Submission creation locks the problem and snapshots its limits and tests, so author edits cannot change queued work. Deletion disables a problem while preserving history.

The submission flow is browser → API → PostgreSQL submission and outbox transaction → outbox publisher → Redis Stream → leased worker attempt → Docker sandbox → PostgreSQL result → owner-authenticated SSE. Python and C++ are the initial languages. Expected outputs stay in the worker.

Redis Streams carry submission IDs only. Each worker consumer obtains a database lease and a unique attempt ID before execution. Heartbeats renew live leases, every transition is fenced by the current attempt ID, and a redelivery of terminal work is acknowledged without execution. Expired Redis pending entries and database leases let another worker safely resume work after a crash.

Submission creation inserts its outbox event in the same database transaction. Concurrent publishers lease rows with `SKIP LOCKED`, publish them, and then mark them sent. Duplicate publication is safe because judging is idempotent. Transient infrastructure failures create delayed outbox events with bounded exponential backoff; the third failed attempt is stored as an internal failure and moved atomically to the dead-letter stream. Deterministic user verdicts are never retried.

Workers run bounded pools and may be replicated as independent processes. A worker compiles or syntax-checks source, evaluates cases, writes the aggregate in PostgreSQL, and acknowledges Redis only after the durable result. A fixed launch marker starts execution deadlines after Docker startup; no submitted text is inserted into shell commands.

Compilation uses its own container and budget. The worker transfers only a bounded source or compiled artifact to a fresh container for each test. Host cgroup v2 counters supply memory and OOM evidence; writable sandbox files cannot supply verdict signals. Docker enforces network denial, read-only root, UID/capability restrictions, CPU/memory/PID limits, and bounded temporary filesystems. A synchronized output budget caps stdout and stderr together and cancels flooding execution.

Redis also owns expiring rate-limit counters for authentication and submissions. Public reads remain independent of Redis. Owner-authenticated SSE reads authoritative PostgreSQL state and ends at a terminal status. The test workflow runs real PostgreSQL, Redis, Docker golden/security tests, Go race/vet checks, frontend tests/build, and dependency scans.

Kubernetes, contests, and operational dashboards belong to later phases.
