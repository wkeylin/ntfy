# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ntfy is an HTTP-based pub-sub notification service written in Go with a React web frontend. Users publish messages via PUT/POST to topic URLs and subscribe via GET/WebSocket/SSE. The server supports SQLite and PostgreSQL backends, Firebase Cloud Messaging (FCM) for Android push, APNS (via FCM) for iOS, Huawei Push Kit for HarmonyOS, Web Push, SMTP for email notifications, and Stripe for payments.

## Common Commands

### Build
- `make cli-darwin-server` — Build server+client binary for macOS development (outputs to `dist/ntfy_darwin_server/ntfy`)
- `make cli-linux-server` — Build server+client binary for Linux (no GoReleaser needed)
- `make web` — Build web app (runs `npm install` + `vite build`, output goes to `server/site/`)
- `make docs` — Build documentation (mkdocs, requires Python venv)
- `make build` — Build everything (web + docs + CLI, slow)

### Test
- `make test` — Run all Go tests (excludes `test/`, `examples/`, `tools/` dirs)
- `make testv` — Run all Go tests with verbose output
- `go test ./server/` — Run tests for a single package
- `go test ./server/ -run TestServer_PublishAndPoll` — Run a single test
- `make race` — Run tests with `-race` flag
- `make coverage` — Run tests with coverage report

### Lint & Format
- `make check` — Run all checks: tests + web format check + go fmt + go vet + web lint + golint + staticcheck
- `make fmt` — Run `gofmt` and prettier (web)
- `make vet` — Run `go vet`
- `make lint` — Run `golint`
- `make staticcheck` — Run `staticcheck`
- `make web-lint` — ESLint on web app
- `make web-fmt` — Prettier on web app

### PostgreSQL Tests
Tests run against both SQLite and PostgreSQL when `NTFY_TEST_DATABASE_URL` is set:
```
export NTFY_TEST_DATABASE_URL="postgres://ntfy:ntfy@localhost:5432/ntfy_test?sslmode=disable"
```
Without this env var, PostgreSQL tests are skipped automatically.

## Architecture

### Go Backend (`heckel.io/ntfy/v2`)

**Entry point:** `main.go` → `cmd.New()` creates the CLI app (urfave/cli/v2).

**`cmd/`** — CLI commands. Uses `init()` functions to register commands into a global `commands` slice. Server commands use `//go:build !noserver` build tag. Key commands: `serve`, `publish`, `subscribe`, `user`, `tier`, `token`, `access`.

**`server/`** — Core server logic. `Server` struct in `server.go` is the central type, handling HTTP routing, WebSocket, SSE, SMTP, and Firebase integration. Key files:
- `server.go` — Main server, HTTP handler routing, pub/sub logic
- `server_account.go` — User account API endpoints
- `server_admin.go` — Admin API endpoints
- `server_firebase.go` — FCM push notification forwarding
- `server_payments.go` — Stripe billing integration
- `server_webpush.go` — Web Push notifications
- `server_huaweipush.go` — Huawei Push Kit (HarmonyOS) notifications
- `server_matrix.go` — Matrix Push Gateway support
- `server_manager.go` — Background manager (pruning, stats)
- `server_middleware.go` — Rate limiting middleware
- `server_metrics.go` — Prometheus metrics
- `config.go` — Server configuration with defaults
- `visitor.go` — Per-visitor rate limiting and state tracking
- `topic.go` — Topic subscription management
- `smtp_server.go` / `smtp_sender.go` — Inbound/outbound email

**`user/`** — User/auth management. `Manager` handles user accounts, tokens, ACLs, tiers. Dual backend: `manager_sqlite.go` and `manager_postgres.go` with separate schema files.

**`message/`** — Message cache (stores published messages for polling). Dual backend: `cache_sqlite.go` and `cache_postgres.go`.

**`webpush/`** — Web Push subscription store. Dual backend: `store_sqlite.go` and `store_postgres.go`.

**`huaweipush/`** — Huawei Push Kit (HarmonyOS NEXT) integration. Token-based push (V3 API has no topic support). Contains `Client` (OAuth2 token management + batch sending) and `Store` (push token → topics mapping). Dual backend: `store_sqlite.go` and `store_postgres.go`. Unlike Firebase (topic broadcast, no storage), this module stores device tokens and queries them per-topic at publish time, similar to `webpush/`.

**`client/`** — Go client library for subscribing to topics.

**`model/`** — Shared data types (`Message`, `Attachment`, `Action`, etc.).

**`log/`** — Custom structured logging package with level overrides.

**`util/`** — Shared utilities (rate limiting, batching queue, gzip handler, etc.).

**`payments/`** — Stripe payment integration.

**`db/`** — Database helpers. `db.go` has `ExecTx`/`QueryTx` transaction wrappers. `db/pg/` has PostgreSQL connection pooling via pgx. `db/test/` has test helpers for creating temporary PostgreSQL schemas.

### Build Tags

- `noserver` — Exclude server code, build client-only binary (CGO_ENABLED=0)
- `nofirebase` — Exclude Firebase/FCM support (dummy implementations provided)
- `nopayments` — Exclude Stripe payment support (dummy implementations provided)
- `nowebpush` — Exclude Web Push support (dummy implementations provided)
- `nohuaweipush` — Exclude Huawei Push Kit support (dummy implementations provided)
- `sqlite_omit_load_extension,osusergo,netgo` — Used for static builds

Each optional feature has a real implementation file and a `_dummy.go` file with the corresponding negated build tag.

### Web Frontend (`web/`)

React app using Material UI, Vite for bundling, Dexie (IndexedDB) for local storage. Key structure:
- `src/components/` — React components (App, Messaging, Navigation, Notifications, etc.)
- `src/app/` — Business logic (Api.js, ConnectionManager.js, SubscriptionManager.js, etc.)
- i18n support via react-i18next

The built web app is embedded into the Go binary via `//go:embed` (placed in `server/site/`).

### Database Architecture

All database-backed stores (messages, users, web push, huawei push) support dual backends:
- **SQLite** — Default, file-based. Configured via `cache-file`, `auth-file`, `web-push-file`.
- **PostgreSQL** — Configured via single `database-url`. When set, all stores use PostgreSQL and the individual file options must not be set.

Tests use `forEachBackend()` helper to run against both SQLite and PostgreSQL.

### Test Patterns

Tests use `stretchr/testify` (primarily `require`). Server tests create in-memory test servers via `newTestServer(t, newTestConfig(t, databaseURL))` and make HTTP requests using a `request()` helper. The `forEachBackend()` pattern runs each test against both SQLite and PostgreSQL backends.
