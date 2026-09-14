# Online Coding Judge

A Go platform for programming problems and asynchronous Python and C++ evaluation in isolated Docker containers.

Stack: Go 1.26.7+, Gin, pgx/PostgreSQL, Redis Streams, Docker, Kubernetes, React/Next.js, and Node.js 22+.

Features include registration and login, staff problem authoring, public samples, hidden tests, bounded source submissions, owner-only live results, durable queue publication, and horizontally concurrent judging. See [ROADMAP.md](ROADMAP.md) for status and [ARCHITECTURE.md](ARCHITECTURE.md) for service boundaries.

Phases 1–5 are implemented and tested: core platform, judging, sandbox security controls, reliable multi-worker scale, and Kubernetes deployment. Product-depth and operations phases are not implemented.

## Development

Use Linux or WSL2 with Go, PostgreSQL, Redis, Docker, and Node.js. Create a database and apply migrations in order:

```sh
export DATABASE_URL='postgres://judge:your-password@localhost:5432/judge?sslmode=disable'
for migration in db/migrations/*.sql; do
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$migration"
done
export REDIS_ADDR=127.0.0.1:6379
go run ./cmd/api
```

In another terminal:

```sh
cd web
npm ci
npm run dev
```

Open http://localhost:3000. Next.js proxies `/v1` to `http://127.0.0.1:8080`; override `API_ORIGIN` as needed. Register an account, then grant a trusted staff account the admin role using a database administrator connection:

```sql
UPDATE users SET role = 'admin' WHERE email = 'your-admin@example.com';
```

Sessions expire after 24 hours; the browser keeps tokens in memory. Use HTTPS at the deployment edge. The API binds to loopback; set `API_ADDR` explicitly to expose it.

## Judge workers

Build the language images on the Docker host, then start a worker process:

```sh
docker build -t judge-python:1 language-images/python
docker build -t judge-cpp:1 language-images/cpp
export PYTHON_IMAGE=$(docker image inspect judge-python:1 --format '{{.Id}}')
export CPP_IMAGE=$(docker image inspect judge-cpp:1 --format '{{.Id}}')
export WORKER_CONCURRENCY=3
go run ./cmd/worker
```

The worker needs the same `DATABASE_URL` and `REDIS_ADDR` as the API and access to a local Linux Docker daemon. It syntax-checks Python or compiles C++23, runs visible and hidden tests, compares whitespace-separated tokens, and persists the first failing verdict or Accepted. The browser streams pending status changes and retains a manual refresh control.

`WORKER_CONCURRENCY` accepts 1–64; the default is 3. Multiple worker processes may run concurrently. PostgreSQL leases and attempt IDs fence stale workers, Redis pending entries are reclaimed after lease expiry, and transient infrastructure failures use three bounded attempts before entering `judge:submissions:dead`. Submission creation and its outbox event commit together, so Redis outages do not lose accepted work.

## Kubernetes deployment

The Kustomize bundle in `deploy/k8s` deploys three API replicas and at least three worker replicas in the `coding-judge` namespace. It includes the API service and TLS ingress, default-deny network policy with explicit control-plane paths, an API disruption budget, and a CPU HPA that scales workers from 3 to 30. PostgreSQL and Redis remain external dependencies.

Build and publish immutable application image tags, apply both database migrations to the target PostgreSQL database, then replace the application image references, ingress host/TLS secret, Redis address, and certified language image IDs in the manifests. Provision the database URL separately:

```sh
VERSION=$(git rev-parse HEAD)
docker build --target api -t registry.example/judge-api:$VERSION .
docker build --target worker -t registry.example/judge-worker:$VERSION .
docker push registry.example/judge-api:$VERSION
docker push registry.example/judge-worker:$VERSION

kubectl apply -f deploy/k8s/namespace.yaml
kubectl -n coding-judge create secret generic judge-runtime-secrets \
  --from-literal=database-url="$DATABASE_URL"
kubectl apply -k deploy/k8s
kubectl -n coding-judge rollout status deployment/judge-api
kubectl -n coding-judge rollout status deployment/judge-worker
```

The cluster needs an ingress controller and metrics-server. Label dedicated Linux worker nodes with `workload=judge` and taint them `dedicated=judge:NoSchedule`. Those nodes need cgroup v2, a Docker-compatible daemon at `/var/run/docker.sock`, and the certified Python/C++ image IDs loaded into that daemon. The socket must be accessible to group ID 65532, which is the worker image group. The trusted worker pod receives host PID and Docker access so it can create and monitor sandboxes; submitted containers still receive no host mounts, credentials, network, or service-account token.

The supplied egress policies allow DNS plus TCP 5432/6379 to accommodate managed PostgreSQL and Redis. Narrow their destination CIDRs and adjust the ingress-controller namespace selector for the target cluster before production use. The manifests deliberately omit a worker disruption budget so judge nodes remain maintainable; leased jobs recover through Phase 4 redelivery.

## Tests

Use a dedicated test database whose user can create schemas:

```sh
export TEST_DATABASE_URL='postgres://judge_test:your-password@localhost:5432/judge_test?sslmode=disable'
export TEST_REDIS_ADDR=127.0.0.1:6379
go test -race -p 1 ./cmd/... ./internal/... ./tests/...
go vet ./cmd/... ./internal/... ./tests/...
cd web
npm test
npm run build
```

Database and Redis tests skip when their environment variables are absent. For real language execution and sandbox security tests, also set `TEST_DOCKER=1`, `PYTHON_IMAGE`, and `CPP_IMAGE`. Runtime image IDs are immutable; tags are used only to locate those IDs after a local build. No demo deployment or screenshots are available yet.

## Sandbox security

The worker requires a local Linux Docker daemon with cgroup v2 and readable `/proc/<container-pid>/cgroup`, `memory.peak`, and `memory.events` files. Monitoring failures stop evaluation rather than silently disabling limits. The API needs neither Docker access nor cgroup access.

| Control | Enforced behavior |
| --- | --- |
| Identity | UID/GID 65534, all capabilities dropped, no new privileges, Docker default seccomp |
| Network | No container network access |
| Filesystem | Read-only root; 32 MiB `/work`, 16 MiB non-executable `/tmp`; no host mounts |
| Resources | One CPU, 64 PIDs, exact problem memory limit, no extra swap |
| Execution | Separate compiler budget: 15 seconds and 256 MiB; fresh container per test |
| Output/files | 1 MiB combined stdout/stderr; 16 MiB per file; bounded compiler artifact transfer |
| Secrets | No host environment, service credentials, or Docker socket in submission containers |
| Results | Kernel OOM attribution; aggregate peak sandbox memory; no hidden diagnostics in APIs |

Memory measurements include the runtime container's working files and processes. Container startup time is excluded from the execution deadline. Containers are forcibly removed after each operation, including cancellation. Docker shares the host kernel; these controls do not claim protection against every kernel exploit.

Authentication is limited to 20 requests per minute per direct client address; submissions to 10 per minute per account. Redis outages reject these rate-limited writes while public reads remain available. Forwarded client-address headers are untrusted by default.

Language base images are digest-pinned. Runtime builds apply available distribution security updates, so rebuilt image IDs can change and must be recertified with the golden/security suite. Preserve certified image IDs for repeatable execution. CI builds and rejects fixable high/critical findings in both language and application images. Upstream OS findings without published fixes remain; this milestone is not a production security certification. Image signing and production release approval are not configured.
