# HarmonyOS Push Kit Integration Design

## Overview

Extend ntfy server to support Huawei HarmonyOS NEXT Push Kit as a new push notification channel. The design follows the existing modular pattern (independent package + build tag + dummy file) and uses token-based pushing since HarmonyOS V3 API does not support topic-based messaging.

## Constraints

### HarmonyOS Push Kit V3 API

- **Endpoint**: `POST https://push-api.cloud.huawei.com/v3/{projectId}/messages:send`
- **Auth**: OAuth 2.0 Client Credentials → Bearer Token (1 hour TTL, rate limit 1000 req/5min)
- **Target**: Token array only (no topic support), max 1000 tokens per request
- **Message size**: 4096 bytes max
- **push-type**: 0=notification, 1=card refresh, 6=background message, 7=live window
- **Frequency control**: 3000 messages/device/day (production), enforced by Huawei server-side
- **Token lifecycle**: Invalidated on app uninstall, factory reset, or AAID change; error-code-driven cleanup required

### Design Decisions

- **Architecture**: Independent `huaweipush/` package (Option A), not refactoring WebPush into a shared abstraction
- **Scope**: HarmonyOS NEXT Push Kit V3 first, extensible for HMS Core Push Kit V1 later
- **Deployment**: Single Huawei app (one set of credentials), with `project_id` field for future multi-app support

## Data Model

### Table: `huawei_push_subscriptions`

| Column | Type | Description |
|---|---|---|
| `push_token` | TEXT PK | Huawei device Push Token (unique per device per app) |
| `project_id` | TEXT NOT NULL | Huawei projectId (single value now, multi-app later) |
| `topics` | TEXT | Comma-separated subscribed topic list |
| `updated_at` | DATETIME | Last registration/heartbeat time, used for expiry |

### Store Interface

```go
type Store struct { db *sql.DB }

func (s *Store) UpsertSubscription(pushToken, projectID string, topics []string) error
func (s *Store) RemoveSubscription(pushToken string) error
func (s *Store) RemoveByTokens(pushTokens []string) error
func (s *Store) SubscriptionsForTopic(topic string) ([]string, error)
func (s *Store) ExpireSubscriptions(olderThan time.Duration) (int64, error)
func (s *Store) Close() error
```

Dual backend: SQLite (`store_sqlite.go`) and PostgreSQL (`store_postgres.go`) with separate schema files.

### Token Invalidation

Unlike WebPush (TTL-based expiry), HarmonyOS uses **error-code-driven cleanup**: after batch sending, invalid tokens reported by Huawei are removed via `RemoveByTokens`. Periodic expiry via `updated_at` handles abandoned registrations.

## OAuth Token Management & Push Client

### Client Structure

```go
type Client struct {
    projectID    string
    clientID     string
    clientSecret string
    tokenURL     string        // Default: https://oauth-login.cloud.huawei.com/oauth2/v3/token
    pushURL      string        // Default: https://push-api.cloud.huawei.com/v3/{projectId}/messages:send
    accessToken  string
    tokenExpiry  time.Time
    httpClient   *http.Client  // Injectable for testing
    mu           sync.Mutex
}
```

- `getAccessToken()`: Checks cached token validity (refresh 5 min before expiry), mutex-protected
- `tokenURL` and `pushURL` are configurable for testing and future HMS V1 extension

### Send Method

```go
func (c *Client) Send(tokens []string, msg *HuaweiMessage) (*SendResult, error)

type SendResult struct {
    SuccessCount  int
    FailureCount  int
    InvalidTokens []string  // Tokens to remove from store
}
```

Flow: get token → convert message → batch by 1000 → send → collect invalid tokens → return result.

### Message Format Mapping

| ntfy field | Huawei V3 field | Notes |
|---|---|---|
| `event=message` | `push-type: 0`, payload.notification | Notification message |
| `event=keepalive/poll_request` | `push-type: 6`, payload.data | Background message (silent wakeup) |
| `title` | `notification.title` | Title |
| `message` | `notification.body` | Body, truncated if too long |
| `priority >= 4` | High priority category | Priority mapping |
| Other fields (tags, click, actions, etc.) | `notification.data` or custom fields | Passed through for client processing |

## Server Integration

### Config

New fields in `server/config.go`:

```go
HuaweiPushProjectID    string
HuaweiPushClientID     string
HuaweiPushClientSecret string
```

CLI flags + env vars + YAML config, enabled when all three are non-empty.

| CLI flag | Env var |
|---|---|
| `--huawei-push-project-id` | `NTFY_HUAWEI_PUSH_PROJECT_ID` |
| `--huawei-push-client-id` | `NTFY_HUAWEI_PUSH_CLIENT_ID` |
| `--huawei-push-client-secret` | `NTFY_HUAWEI_PUSH_CLIENT_SECRET` |

### Build Tags

```
server/server_huaweipush.go         //go:build !nohuaweipush
server/server_huaweipush_dummy.go   //go:build nohuaweipush
```

### API Endpoints

**`POST /v1/huaweipush`** — Register/update subscription

```json
{
  "push_token": "IQAAAA**********4Tw",
  "topics": ["mytopic", "alerts"]
}
```

**`DELETE /v1/huaweipush`** — Unregister

```json
{
  "push_token": "IQAAAA**********4Tw"
}
```

`project_id` is filled server-side from config. ACL checks apply if topics have access control.

### Server Struct

```go
huaweiPushClient *huaweipush.Client  // nil when disabled
huaweiPushStore  *huaweipush.Store   // nil when disabled
```

### Publish Flow Integration

Added alongside existing Firebase/WebPush at all three trigger points (publish, update/delete, delayed send):

```go
if s.huaweiPushClient != nil {
    go s.sendToHuaweiPush(v, m)
}
```

`sendToHuaweiPush`: query store for topic → batch send → cleanup invalid tokens → log + metrics.

### Metrics

```
ntfy_huawei_push_published_success
ntfy_huawei_push_published_failure
```

### Expiry Cleanup

Added to `server_manager.go` periodic tasks, reusing `ManagerInterval`.

## File Structure

### New Files

```
huaweipush/
  store.go
  store_sqlite.go
  store_sqlite_schema.go
  store_postgres.go
  store_postgres_schema.go
  store_test.go
  client.go
  client_test.go
  types.go
server/
  server_huaweipush.go
  server_huaweipush_dummy.go
  server_huaweipush_test.go
```

### Modified Files

| File | Change |
|---|---|
| `server/config.go` | Add HuaweiPush config fields + defaults |
| `server/server.go` | Add struct fields, init in New(), add push trigger calls |
| `server/server_manager.go` | Add Huawei subscription expiry to periodic cleanup |
| `server/server_metrics.go` | Add two Prometheus counters |
| `cmd/serve.go` | Add CLI flags and config loading |
| `server/server.yml` | Add Huawei push config comments |

### Unchanged

- `server_firebase.go` — untouched
- `webpush/` — untouched
- `web/` — not involved (HarmonyOS client is a separate app)
- `client/` — not involved
