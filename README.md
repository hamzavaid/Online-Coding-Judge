# Online Coding Judge

A Go platform for programming problems and asynchronous evaluation of submitted code, targeting Python and C++ with isolated Docker execution.

Stack: Go 1.26+, Gin, pgx/PostgreSQL, Redis Streams, Docker, React/Next.js, Node.js 22+.

Features include registration/login, staff problem authoring, public samples, hidden tests, bounded source submissions, history, and owner-only results. See [ROADMAP.md](ROADMAP.md) for status and [ARCHITECTURE.md](ARCHITECTURE.md) for service boundaries.

## Development

Use Linux or WSL2 with Go, PostgreSQL, and Node.js. Create a database and apply the migration once:

```sh
export DATABASE_URL='postgres://judge:your-password@localhost:5432/judge?sslmode=disable'
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f db/migrations/001_core.sql
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

## Tests

Use a dedicated test database whose user can create schemas:

```sh
export TEST_DATABASE_URL='postgres://judge_test:your-password@localhost:5432/judge_test?sslmode=disable'
go test -race ./...
go vet ./...
cd web
npm test
npm run build
```

Database tests skip when the environment variable is absent. Unit tests alone are not a complete integration check. No demo deployment or screenshots are available yet.
