# Roadmap

| Phase | Scope | Status |
| --- | --- | --- |
| 1 — Core platform | Auth, problem CRUD, PostgreSQL, submissions, basic UI | Complete |
| 2 — Judge MVP | Redis, single worker, Python/C++, tests, verdicts | Complete |
| 3 — Isolation | Docker limits, network denial, output caps, security tests | Complete |
| 4 — Scale | Multiple workers, retries, DLQ, outbox, live updates | Future |
| 5 — Kubernetes | Deployments, networking, autoscaling, dedicated nodes | Future |
| 6 — Product depth | Contests, leaderboards, rejudge, runtime management | Future |
| 7 — Operations | Dashboards, alerts, tracing, load and chaos tests | Future |

Current implementation: Phases 1–3. Work stops at the isolation milestone; later phases have not begun.
