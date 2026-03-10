# HarmonyOS Push Kit Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add HarmonyOS NEXT Push Kit V3 as a server-side push notification channel, following ntfy's existing modular architecture.

**Architecture:** Independent `huaweipush/` package with SQLite/PostgreSQL dual-backend store, OAuth2 client, and HTTP push sender. Server integration via build tags (`nohuaweipush`) with dummy fallback. Token-based push model (V3 API has no topic support).

**Tech Stack:** Go standard library, `database/sql`, `net/http`, `stretchr/testify`, existing `heckel.io/ntfy/v2/db` helpers.

**Design doc:** `docs/plans/2026-03-10-huawei-push-design.md`

---

### Task 1: huaweipush types

**Files:**
- Create: `huaweipush/types.go`

**Step 1: Create types file**

```go
package huaweipush

// huaweiPushMessageLimit is the max payload size for Huawei Push Kit V3 messages.
const huaweiPushMessageLimit = 4096

// huaweiPushTokenBatchSize is the max number of tokens per push request.
const huaweiPushTokenBatchSize = 1000

// DefaultTokenURL is the default OAuth 2.0 token endpoint.
const DefaultTokenURL = "https://oauth-login.cloud.huawei.com/oauth2/v3/token"

// DefaultPushURL is the default push API endpoint template.
// The {projectId} placeholder must be replaced with the actual project ID.
const DefaultPushURL = "https://push-api.cloud.huawei.com/v3/{projectId}/messages:send"

// SendResult represents the result of a batch push send.
type SendResult struct {
	SuccessCount  int
	FailureCount  int
	InvalidTokens []string // Tokens that should be removed from the store
}
```

**Step 2: Verify it compiles**

Run: `go build ./huaweipush/`
Expected: PASS

**Step 3: Commit**

```
git add huaweipush/types.go
git commit -m "feat(huaweipush): add types package with constants and SendResult"
```

---

### Task 2: huaweipush store — SQLite backend

**Files:**
- Create: `huaweipush/store.go`
- Create: `huaweipush/store_sqlite.go`

**Step 1: Write store.go with Store struct and methods**

```go
package huaweipush

import (
	"database/sql"
	"strings"
	"time"

	"heckel.io/ntfy/v2/db"
)

// Store holds the database connection and queries for Huawei push subscriptions.
type Store struct {
	db      *sql.DB
	queries queries
}

type queries struct {
	upsertSubscription  string
	deleteByToken       string
	deleteByTokens      string
	selectForTopic      string
	deleteExpired       string
	updateUpdatedAt     string
}

// UpsertSubscription adds or updates a push token's subscription to the given topics.
func (s *Store) UpsertSubscription(pushToken, projectID string, topics []string) error {
	_, err := s.db.Exec(s.queries.upsertSubscription, pushToken, projectID, strings.Join(topics, ","), time.Now().Unix())
	return err
}

// RemoveSubscription removes a push token's subscription.
func (s *Store) RemoveSubscription(pushToken string) error {
	_, err := s.db.Exec(s.queries.deleteByToken, pushToken)
	return err
}

// RemoveByTokens removes subscriptions for all given push tokens (used for error-code-driven cleanup).
func (s *Store) RemoveByTokens(pushTokens []string) error {
	if len(pushTokens) == 0 {
		return nil
	}
	return db.ExecTx(s.db, func(tx *sql.Tx) error {
		for _, token := range pushTokens {
			if _, err := tx.Exec(s.queries.deleteByToken, token); err != nil {
				return err
			}
		}
		return nil
	})
}

// SubscriptionsForTopic returns all push tokens subscribed to the given topic.
func (s *Store) SubscriptionsForTopic(topic string) ([]string, error) {
	rows, err := s.db.Query(s.queries.selectForTopic, "%,"+topic+",%", topic+",%", "%,"+topic, topic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := make([]string, 0)
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// ExpireSubscriptions removes subscriptions not updated within the given duration.
func (s *Store) ExpireSubscriptions(olderThan time.Duration) (int64, error) {
	result, err := s.db.Exec(s.queries.deleteExpired, time.Now().Add(-olderThan).Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// SetSubscriptionUpdatedAt updates the updated_at timestamp (exported for testing).
func (s *Store) SetSubscriptionUpdatedAt(pushToken string, updatedAt int64) error {
	_, err := s.db.Exec(s.queries.updateUpdatedAt, updatedAt, pushToken)
	return err
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
```

**Step 2: Write store_sqlite.go**

```go
package huaweipush

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"

	"heckel.io/ntfy/v2/db"
)

const (
	sqliteCreateTablesQuery = `
		CREATE TABLE IF NOT EXISTS huawei_push_subscription (
			push_token TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			topics TEXT NOT NULL,
			updated_at INT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_hwpush_updated_at ON huawei_push_subscription (updated_at);
		CREATE TABLE IF NOT EXISTS schemaVersion (
			id INT PRIMARY KEY,
			version INT NOT NULL
		);
	`

	sqliteUpsertQuery = `
		INSERT INTO huawei_push_subscription (push_token, project_id, topics, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (push_token)
		DO UPDATE SET project_id = excluded.project_id, topics = excluded.topics, updated_at = excluded.updated_at
	`
	sqliteDeleteByTokenQuery   = `DELETE FROM huawei_push_subscription WHERE push_token = ?`
	sqliteSelectForTopicQuery  = `SELECT push_token FROM huawei_push_subscription WHERE topics LIKE ? OR topics LIKE ? OR topics LIKE ? OR topics = ?`
	sqliteDeleteExpiredQuery   = `DELETE FROM huawei_push_subscription WHERE updated_at <= ?`
	sqliteUpdateUpdatedAtQuery = `UPDATE huawei_push_subscription SET updated_at = ? WHERE push_token = ?`
)

const (
	sqliteCurrentSchemaVersion     = 1
	sqliteInsertSchemaVersionQuery = `INSERT INTO schemaVersion VALUES (1, ?)`
	sqliteSelectSchemaVersionQuery = `SELECT version FROM schemaVersion WHERE id = 1`
)

// NewSQLiteStore creates a new SQLite-backed Huawei push store.
func NewSQLiteStore(filename string) (*Store, error) {
	db, err := sql.Open("sqlite3", filename)
	if err != nil {
		return nil, err
	}
	if err := setupSQLite(db); err != nil {
		return nil, err
	}
	return &Store{
		db: db,
		queries: queries{
			upsertSubscription: sqliteUpsertQuery,
			deleteByToken:      sqliteDeleteByTokenQuery,
			selectForTopic:     sqliteSelectForTopicQuery,
			deleteExpired:      sqliteDeleteExpiredQuery,
			updateUpdatedAt:    sqliteUpdateUpdatedAtQuery,
		},
	}, nil
}

func setupSQLite(db *sql.DB) error {
	var schemaVersion int
	if err := db.QueryRow(sqliteSelectSchemaVersionQuery).Scan(&schemaVersion); err != nil {
		return setupNewSQLite(db)
	} else if schemaVersion > sqliteCurrentSchemaVersion {
		return fmt.Errorf("unexpected schema version: version %d is higher than current version %d", schemaVersion, sqliteCurrentSchemaVersion)
	}
	return nil
}

func setupNewSQLite(sqlDB *sql.DB) error {
	return db.ExecTx(sqlDB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(sqliteCreateTablesQuery); err != nil {
			return err
		}
		if _, err := tx.Exec(sqliteInsertSchemaVersionQuery, sqliteCurrentSchemaVersion); err != nil {
			return err
		}
		return nil
	})
}
```

**Step 3: Verify it compiles**

Run: `go build ./huaweipush/`
Expected: PASS

**Step 4: Commit**

```
git add huaweipush/store.go huaweipush/store_sqlite.go
git commit -m "feat(huaweipush): add Store with SQLite backend"
```

---

### Task 3: huaweipush store — PostgreSQL backend

**Files:**
- Create: `huaweipush/store_postgres.go`

**Step 1: Write store_postgres.go**

```go
package huaweipush

import (
	"database/sql"
	"fmt"

	"heckel.io/ntfy/v2/db"
)

const (
	postgresCreateTablesQuery = `
		CREATE TABLE IF NOT EXISTS huawei_push_subscription (
			push_token TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			topics TEXT NOT NULL,
			updated_at BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_hwpush_updated_at ON huawei_push_subscription (updated_at);
		CREATE TABLE IF NOT EXISTS schema_version (
			store TEXT PRIMARY KEY,
			version INT NOT NULL
		);
	`

	postgresUpsertQuery = `
		INSERT INTO huawei_push_subscription (push_token, project_id, topics, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (push_token)
		DO UPDATE SET project_id = excluded.project_id, topics = excluded.topics, updated_at = excluded.updated_at
	`
	postgresDeleteByTokenQuery   = `DELETE FROM huawei_push_subscription WHERE push_token = $1`
	postgresSelectForTopicQuery  = `SELECT push_token FROM huawei_push_subscription WHERE topics LIKE $1 OR topics LIKE $2 OR topics LIKE $3 OR topics = $4`
	postgresDeleteExpiredQuery   = `DELETE FROM huawei_push_subscription WHERE updated_at <= $1`
	postgresUpdateUpdatedAtQuery = `UPDATE huawei_push_subscription SET updated_at = $1 WHERE push_token = $2`
)

const (
	pgCurrentSchemaVersion           = 1
	postgresInsertSchemaVersionQuery = `INSERT INTO schema_version (store, version) VALUES ('huawei_push', $1)`
	postgresSelectSchemaVersionQuery = `SELECT version FROM schema_version WHERE store = 'huawei_push'`
)

// NewPostgresStore creates a new PostgreSQL-backed Huawei push store using an existing connection pool.
func NewPostgresStore(db *sql.DB) (*Store, error) {
	if err := setupPostgres(db); err != nil {
		return nil, err
	}
	return &Store{
		db: db,
		queries: queries{
			upsertSubscription: postgresUpsertQuery,
			deleteByToken:      postgresDeleteByTokenQuery,
			selectForTopic:     postgresSelectForTopicQuery,
			deleteExpired:      postgresDeleteExpiredQuery,
			updateUpdatedAt:    postgresUpdateUpdatedAtQuery,
		},
	}, nil
}

func setupPostgres(db *sql.DB) error {
	var schemaVersion int
	err := db.QueryRow(postgresSelectSchemaVersionQuery).Scan(&schemaVersion)
	if err != nil {
		return setupNewPostgres(db)
	}
	if schemaVersion > pgCurrentSchemaVersion {
		return fmt.Errorf("unexpected schema version: version %d is higher than current version %d", schemaVersion, pgCurrentSchemaVersion)
	}
	return nil
}

func setupNewPostgres(sqlDB *sql.DB) error {
	return db.ExecTx(sqlDB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(postgresCreateTablesQuery); err != nil {
			return err
		}
		if _, err := tx.Exec(postgresInsertSchemaVersionQuery, pgCurrentSchemaVersion); err != nil {
			return err
		}
		return nil
	})
}
```

**Step 2: Verify it compiles**

Run: `go build ./huaweipush/`
Expected: PASS

**Step 3: Commit**

```
git add huaweipush/store_postgres.go
git commit -m "feat(huaweipush): add PostgreSQL store backend"
```

---

### Task 4: huaweipush store — tests

**Files:**
- Create: `huaweipush/store_test.go`

**Step 1: Write store tests**

```go
package huaweipush_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	dbtest "heckel.io/ntfy/v2/db/test"
	"heckel.io/ntfy/v2/huaweipush"
)

func forEachBackend(t *testing.T, f func(t *testing.T, store *huaweipush.Store)) {
	t.Run("sqlite", func(t *testing.T) {
		store, err := huaweipush.NewSQLiteStore(filepath.Join(t.TempDir(), "huaweipush.db"))
		require.Nil(t, err)
		t.Cleanup(func() { store.Close() })
		f(t, store)
	})
	t.Run("postgres", func(t *testing.T) {
		testDB := dbtest.CreateTestPostgres(t)
		store, err := huaweipush.NewPostgresStore(testDB)
		require.Nil(t, err)
		f(t, store)
	})
}

func TestStore_UpsertAndQuery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "proj1", []string{"topicA", "topicB"}))
		require.Nil(t, store.UpsertSubscription("token2", "proj1", []string{"topicA"}))

		tokens, err := store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 2)

		tokens, err = store.SubscriptionsForTopic("topicB")
		require.Nil(t, err)
		require.Len(t, tokens, 1)
		require.Equal(t, "token1", tokens[0])

		tokens, err = store.SubscriptionsForTopic("topicC")
		require.Nil(t, err)
		require.Len(t, tokens, 0)
	})
}

func TestStore_UpsertUpdatesTopics(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "proj1", []string{"topicA", "topicB"}))

		tokens, err := store.SubscriptionsForTopic("topicB")
		require.Nil(t, err)
		require.Len(t, tokens, 1)

		// Update to remove topicB
		require.Nil(t, store.UpsertSubscription("token1", "proj1", []string{"topicA"}))

		tokens, err = store.SubscriptionsForTopic("topicB")
		require.Nil(t, err)
		require.Len(t, tokens, 0)

		tokens, err = store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 1)
	})
}

func TestStore_RemoveSubscription(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "proj1", []string{"topicA"}))
		require.Nil(t, store.RemoveSubscription("token1"))

		tokens, err := store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 0)
	})
}

func TestStore_RemoveByTokens(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "proj1", []string{"topicA"}))
		require.Nil(t, store.UpsertSubscription("token2", "proj1", []string{"topicA"}))
		require.Nil(t, store.UpsertSubscription("token3", "proj1", []string{"topicA"}))

		require.Nil(t, store.RemoveByTokens([]string{"token1", "token3"}))

		tokens, err := store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 1)
		require.Equal(t, "token2", tokens[0])
	})
}

func TestStore_RemoveByTokensEmpty(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.RemoveByTokens([]string{}))
		require.Nil(t, store.RemoveByTokens(nil))
	})
}

func TestStore_ExpireSubscriptions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "proj1", []string{"topicA"}))
		require.Nil(t, store.SetSubscriptionUpdatedAt("token1", time.Now().Add(-10*24*time.Hour).Unix()))

		// Should not expire yet
		removed, err := store.ExpireSubscriptions(11 * 24 * time.Hour)
		require.Nil(t, err)
		require.Equal(t, int64(0), removed)

		// Should expire now
		removed, err = store.ExpireSubscriptions(9 * 24 * time.Hour)
		require.Nil(t, err)
		require.Equal(t, int64(1), removed)

		tokens, err := store.SubscriptionsForTopic("topicA")
		require.Nil(t, err)
		require.Len(t, tokens, 0)
	})
}

func TestStore_TopicMatchDoesNotMatchSubstring(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *huaweipush.Store) {
		require.Nil(t, store.UpsertSubscription("token1", "proj1", []string{"test"}))
		require.Nil(t, store.UpsertSubscription("token2", "proj1", []string{"testing"}))

		tokens, err := store.SubscriptionsForTopic("test")
		require.Nil(t, err)
		require.Len(t, tokens, 1)
		require.Equal(t, "token1", tokens[0])
	})
}
```

**Step 2: Run the tests**

Run: `go test ./huaweipush/ -v`
Expected: All SQLite tests PASS, PostgreSQL tests SKIP (unless NTFY_TEST_DATABASE_URL is set)

**Step 3: Commit**

```
git add huaweipush/store_test.go
git commit -m "test(huaweipush): add store tests for SQLite and PostgreSQL"
```

---

### Task 5: huaweipush client — OAuth2 + push sender

**Files:**
- Create: `huaweipush/client.go`

**Step 1: Write client.go**

```go
package huaweipush

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	errHuaweiAccessTokenFailed = errors.New("failed to obtain Huawei access token")
	errHuaweiPushFailed        = errors.New("failed to send Huawei push message")
)

// Client is the Huawei Push Kit V3 API client.
type Client struct {
	projectID    string
	clientID     string
	clientSecret string
	tokenURL     string
	pushURL      string
	accessToken  string
	tokenExpiry  time.Time
	httpClient   *http.Client
	mu           sync.Mutex
}

// NewClient creates a new Huawei Push Kit client.
func NewClient(projectID, clientID, clientSecret string) *Client {
	pushURL := strings.ReplaceAll(DefaultPushURL, "{projectId}", projectID)
	return &Client{
		projectID:    projectID,
		clientID:     clientID,
		clientSecret: clientSecret,
		tokenURL:     DefaultTokenURL,
		pushURL:      pushURL,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// NewClientWithURL creates a client with custom token and push URLs (for testing).
func NewClientWithURL(projectID, clientID, clientSecret, tokenURL, pushURL string) *Client {
	c := NewClient(projectID, clientID, clientSecret)
	c.tokenURL = tokenURL
	c.pushURL = pushURL
	return c
}

// Send sends a push message to the given tokens in batches of huaweiPushTokenBatchSize.
func (c *Client) Send(tokens []string, pushType int, payload json.RawMessage) (*SendResult, error) {
	if len(tokens) == 0 {
		return &SendResult{}, nil
	}
	result := &SendResult{}
	for i := 0; i < len(tokens); i += huaweiPushTokenBatchSize {
		end := i + huaweiPushTokenBatchSize
		if end > len(tokens) {
			end = len(tokens)
		}
		batch := tokens[i:end]
		sr, err := c.sendBatch(batch, pushType, payload)
		if err != nil {
			return result, err
		}
		result.SuccessCount += sr.SuccessCount
		result.FailureCount += sr.FailureCount
		result.InvalidTokens = append(result.InvalidTokens, sr.InvalidTokens...)
	}
	return result, nil
}

func (c *Client) sendBatch(tokens []string, pushType int, payload json.RawMessage) (*SendResult, error) {
	accessToken, err := c.getAccessToken()
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"payload": payload,
		"target":  map[string]any{"token": tokens},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.pushURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("push-type", fmt.Sprintf("%d", pushType))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errHuaweiPushFailed, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d: %s", errHuaweiPushFailed, resp.StatusCode, string(respBody))
	}
	return c.parseSendResponse(respBody, tokens)
}

func (c *Client) parseSendResponse(body []byte, tokens []string) (*SendResult, error) {
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return &SendResult{SuccessCount: len(tokens)}, nil // Best effort
	}
	if resp.Code == "80000000" { // Success
		return &SendResult{SuccessCount: len(tokens)}, nil
	}
	// On error, treat all tokens in this batch as failed
	return &SendResult{FailureCount: len(tokens)}, nil
}

func (c *Client) getAccessToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-5*time.Minute)) {
		return c.accessToken, nil
	}
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	resp, err := c.httpClient.PostForm(c.tokenURL, data)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errHuaweiAccessTokenFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: HTTP %d", errHuaweiAccessTokenFailed, resp.StatusCode)
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("%w: %v", errHuaweiAccessTokenFailed, err)
	}
	c.accessToken = tokenResp.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return c.accessToken, nil
}
```

**Step 2: Verify it compiles**

Run: `go build ./huaweipush/`
Expected: PASS

**Step 3: Commit**

```
git add huaweipush/client.go
git commit -m "feat(huaweipush): add Client with OAuth2 token management and batch push"
```

---

### Task 6: huaweipush client — tests

**Files:**
- Create: `huaweipush/client_test.go`

**Step 1: Write client tests with httptest mock**

```go
package huaweipush_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"heckel.io/ntfy/v2/huaweipush"
)

func TestClient_Send_Success(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token", "expires_in": 3600})
	}))
	defer tokenServer.Close()

	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "0", r.Header.Get("push-type"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": "80000000", "msg": "Success"})
	}))
	defer pushServer.Close()

	client := huaweipush.NewClientWithURL("proj1", "client1", "secret1", tokenServer.URL, pushServer.URL)
	payload, _ := json.Marshal(map[string]any{"notification": map[string]any{"title": "test", "body": "hello"}})
	result, err := client.Send([]string{"token1", "token2"}, 0, payload)
	require.Nil(t, err)
	require.Equal(t, 2, result.SuccessCount)
	require.Equal(t, 0, result.FailureCount)
}

func TestClient_Send_EmptyTokens(t *testing.T) {
	client := huaweipush.NewClient("proj1", "client1", "secret1")
	result, err := client.Send([]string{}, 0, nil)
	require.Nil(t, err)
	require.Equal(t, 0, result.SuccessCount)
}

func TestClient_Send_TokenRefreshCached(t *testing.T) {
	tokenRequests := 0
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenRequests++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "cached-token", "expires_in": 3600})
	}))
	defer tokenServer.Close()

	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": "80000000", "msg": "Success"})
	}))
	defer pushServer.Close()

	client := huaweipush.NewClientWithURL("proj1", "client1", "secret1", tokenServer.URL, pushServer.URL)
	payload, _ := json.Marshal(map[string]any{})

	// Two sends should only request one token
	_, err := client.Send([]string{"token1"}, 0, payload)
	require.Nil(t, err)
	_, err = client.Send([]string{"token2"}, 0, payload)
	require.Nil(t, err)
	require.Equal(t, 1, tokenRequests)
}

func TestClient_Send_Batching(t *testing.T) {
	batchCount := 0
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenServer.Close()

	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batchCount++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		target := body["target"].(map[string]any)
		tokens := target["token"].([]any)
		require.True(t, len(tokens) <= 1000)
		json.NewEncoder(w).Encode(map[string]any{"code": "80000000", "msg": "Success"})
	}))
	defer pushServer.Close()

	client := huaweipush.NewClientWithURL("proj1", "client1", "secret1", tokenServer.URL, pushServer.URL)
	payload, _ := json.Marshal(map[string]any{})

	// Create 2500 tokens: should be split into 3 batches (1000+1000+500)
	tokens := make([]string, 2500)
	for i := range tokens {
		tokens[i] = strings.Repeat("t", 10)
	}
	result, err := client.Send(tokens, 0, payload)
	require.Nil(t, err)
	require.Equal(t, 2500, result.SuccessCount)
	require.Equal(t, 3, batchCount)
}
```

**Step 2: Run tests**

Run: `go test ./huaweipush/ -v -run TestClient`
Expected: All PASS

**Step 3: Commit**

```
git add huaweipush/client_test.go
git commit -m "test(huaweipush): add client tests with httptest mocks"
```

---

### Task 7: Server config + build tag stubs

**Files:**
- Modify: `server/config.go`
- Modify: `server/log.go`
- Create: `server/server_huaweipush_dummy.go`

**Step 1: Add config fields**

In `server/config.go`, add to Config struct (after `WebPushExpiryWarningDuration` line ~186):

```go
HuaweiPushProjectID              string
HuaweiPushClientID               string
HuaweiPushClientSecret           string
HuaweiPushExpiryDuration         time.Duration
```

Add default constant (near line 40):

```go
DefaultHuaweiPushExpiryDuration = 60 * 24 * time.Hour
```

Add to `NewConfig()` (after WebPush entries):

```go
HuaweiPushProjectID:      "",
HuaweiPushClientID:       "",
HuaweiPushClientSecret:   "",
HuaweiPushExpiryDuration: DefaultHuaweiPushExpiryDuration,
```

**Step 2: Add log tag**

In `server/log.go`, add after `tagWebPush`:

```go
tagHuaweiPush = "huawei_push"
```

**Step 3: Create dummy file**

```go
//go:build nohuaweipush

package server

import (
	"net/http"

	"heckel.io/ntfy/v2/model"
)

const (
	HuaweiPushAvailable = false
)

func (s *Server) handleHuaweiPushUpdate(w http.ResponseWriter, r *http.Request, v *visitor) error {
	return errHTTPNotFound
}

func (s *Server) handleHuaweiPushDelete(w http.ResponseWriter, r *http.Request, v *visitor) error {
	return errHTTPNotFound
}

func (s *Server) sendToHuaweiPush(v *visitor, m *model.Message) {
	// Nothing to see here
}

func (s *Server) pruneHuaweiPushSubscriptions() {
	// Nothing to see here
}
```

**Step 4: Verify it compiles**

Run: `go build -tags nohuaweipush ./server/ ./cmd/`
Expected: PASS

**Step 5: Commit**

```
git add server/config.go server/log.go server/server_huaweipush_dummy.go
git commit -m "feat(server): add HuaweiPush config fields, log tag, and build tag dummy"
```

---

### Task 8: Server integration — real implementation

**Files:**
- Create: `server/server_huaweipush.go`
- Modify: `server/server_metrics.go`

**Step 1: Create server_huaweipush.go**

```go
//go:build !nohuaweipush

package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"heckel.io/ntfy/v2/log"
	"heckel.io/ntfy/v2/model"
	"heckel.io/ntfy/v2/user"
)

const (
	HuaweiPushAvailable          = true
	huaweiPushTopicSubscribeLimit = 50
)

func (s *Server) handleHuaweiPushUpdate(w http.ResponseWriter, r *http.Request, v *visitor) error {
	req, err := readJSONWithLimit[apiHuaweiPushUpdateRequest](r.Body, jsonBodyBytesLimit, false)
	if err != nil || req.PushToken == "" {
		return errHTTPBadRequestHuaweiPushSubscriptionInvalid
	} else if len(req.Topics) > huaweiPushTopicSubscribeLimit {
		return errHTTPBadRequestHuaweiPushTopicCountTooHigh
	}
	topics, err := s.topicsFromIDs(req.Topics...)
	if err != nil {
		return err
	}
	if s.userManager != nil {
		u := v.User()
		for _, t := range topics {
			if err := s.userManager.Authorize(u, t.ID, user.PermissionRead); err != nil {
				logvr(v, r).With(t).Err(err).Debug("Access to topic %s not authorized", t.ID)
				return errHTTPForbidden.With(t)
			}
		}
	}
	if err := s.huaweiPushStore.UpsertSubscription(req.PushToken, s.config.HuaweiPushProjectID, req.Topics); err != nil {
		return err
	}
	return s.writeJSON(w, newSuccessResponse())
}

func (s *Server) handleHuaweiPushDelete(w http.ResponseWriter, r *http.Request, v *visitor) error {
	req, err := readJSONWithLimit[apiHuaweiPushUpdateRequest](r.Body, jsonBodyBytesLimit, false)
	if err != nil || req.PushToken == "" {
		return errHTTPBadRequestHuaweiPushSubscriptionInvalid
	}
	if err := s.huaweiPushStore.RemoveSubscription(req.PushToken); err != nil {
		return err
	}
	return s.writeJSON(w, newSuccessResponse())
}

func (s *Server) sendToHuaweiPush(v *visitor, m *model.Message) {
	tokens, err := s.huaweiPushStore.SubscriptionsForTopic(m.Topic)
	if err != nil {
		logvm(v, m).Tag(tagHuaweiPush).Err(err).Warn("Unable to query Huawei push subscriptions")
		return
	}
	if len(tokens) == 0 {
		return
	}
	pushType, payload := toHuaweiPushMessage(m)
	log.Tag(tagHuaweiPush).With(v, m).Debug("Publishing Huawei push message to %d token(s)", len(tokens))
	result, err := s.huaweiPushClient.Send(tokens, pushType, payload)
	if err != nil {
		logvm(v, m).Tag(tagHuaweiPush).Err(err).Warn("Unable to publish Huawei push message")
		minc(metricHuaweiPushPublishedFailure)
		return
	}
	if len(result.InvalidTokens) > 0 {
		if err := s.huaweiPushStore.RemoveByTokens(result.InvalidTokens); err != nil {
			logvm(v, m).Tag(tagHuaweiPush).Err(err).Warn("Unable to remove invalid Huawei push tokens")
		}
	}
	minc(metricHuaweiPushPublishedSuccess)
}

func (s *Server) pruneHuaweiPushSubscriptions() {
	if s.huaweiPushStore == nil {
		return
	}
	removed, err := s.huaweiPushStore.ExpireSubscriptions(s.config.HuaweiPushExpiryDuration)
	if err != nil {
		log.Tag(tagHuaweiPush).Err(err).Warn("Unable to expire Huawei push subscriptions")
		return
	}
	if removed > 0 {
		log.Tag(tagHuaweiPush).Debug("Expired %d Huawei push subscription(s)", removed)
	}
}

func toHuaweiPushMessage(m *model.Message) (int, json.RawMessage) {
	switch m.Event {
	case model.MessageEvent:
		data := map[string]any{
			"id":           m.ID,
			"time":         fmt.Sprintf("%d", m.Time),
			"event":        m.Event,
			"topic":        m.Topic,
			"title":        m.Title,
			"message":      m.Message,
			"priority":     fmt.Sprintf("%d", m.Priority),
			"content_type": m.ContentType,
		}
		if len(m.Tags) > 0 {
			data["tags"] = fmt.Sprintf("%v", m.Tags)
		}
		if m.Click != "" {
			data["click"] = m.Click
		}
		if m.Icon != "" {
			data["icon"] = m.Icon
		}
		body := m.Message
		if len(body) > 100 {
			body = body[:97] + "..."
		}
		payload := map[string]any{
			"notification": map[string]any{
				"category": "SOCIAL_COMMUNICATION",
				"title":    m.Title,
				"body":     body,
				"data":     data,
			},
		}
		raw, _ := json.Marshal(payload)
		return 0, raw // push-type: 0 = notification
	default:
		// keepalive, poll_request, message_delete, message_clear → background message
		data := map[string]any{
			"id":    m.ID,
			"time":  fmt.Sprintf("%d", m.Time),
			"event": m.Event,
			"topic": m.Topic,
		}
		payload := map[string]any{"data": data}
		raw, _ := json.Marshal(payload)
		return 6, raw // push-type: 6 = background message
	}
}
```

**Step 2: Add API request type to server/types.go**

Append to `server/types.go`:

```go
type apiHuaweiPushUpdateRequest struct {
	PushToken string   `json:"push_token"`
	Topics    []string `json:"topics"`
}
```

**Step 3: Add error variables to server/errors.go**

Search for `errHTTPBadRequestWebPushSubscriptionInvalid` in `server/errors.go` and add nearby:

```go
errHTTPBadRequestHuaweiPushSubscriptionInvalid = &errHTTP{40042, http.StatusBadRequest, "invalid request: huawei push payload invalid", "", nil}
errHTTPBadRequestHuaweiPushTopicCountTooHigh   = &errHTTP{40043, http.StatusBadRequest, "invalid request: too many topics for huawei push subscription", "", nil}
```

Use error codes 40042/40043 (verify they are unused first by grepping for them).

**Step 4: Add metrics**

In `server/server_metrics.go`, add variables:

```go
metricHuaweiPushPublishedSuccess prometheus.Counter
metricHuaweiPushPublishedFailure prometheus.Counter
```

In `initMetrics()`, add:

```go
metricHuaweiPushPublishedSuccess = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "ntfy_huawei_push_published_success",
})
metricHuaweiPushPublishedFailure = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "ntfy_huawei_push_published_failure",
})
```

And register them in the `prometheus.MustRegister(...)` call.

**Step 5: Verify it compiles**

Run: `go build ./server/ ./cmd/`
Expected: PASS (may fail until Task 9 wires it up — compile check after Task 9)

**Step 6: Commit**

```
git add server/server_huaweipush.go server/types.go server/errors.go server/server_metrics.go
git commit -m "feat(server): add Huawei push handlers, message conversion, and metrics"
```

---

### Task 9: Server wiring — New(), routing, publish triggers

**Files:**
- Modify: `server/server.go`
- Modify: `server/server_manager.go`

**Step 1: Add fields to Server struct** (in `server/server.go` ~line 61)

After `firebaseClient`:

```go
huaweiPushClient *huaweipush.Client
huaweiPushStore  *huaweipush.Store
```

Add import: `"heckel.io/ntfy/v2/huaweipush"`

**Step 2: Initialize in New()** (after the firebaseClient block, ~line 260)

```go
var huaweiPushClient *huaweipush.Client
var huaweiPushStore *huaweipush.Store
if HuaweiPushAvailable && conf.HuaweiPushProjectID != "" && conf.HuaweiPushClientID != "" && conf.HuaweiPushClientSecret != "" {
	huaweiPushClient = huaweipush.NewClient(conf.HuaweiPushProjectID, conf.HuaweiPushClientID, conf.HuaweiPushClientSecret)
	if pool != nil {
		huaweiPushStore, err = huaweipush.NewPostgresStore(pool)
	} else {
		huaweiPushStore, err = huaweipush.NewSQLiteStore(filepath.Join(filepath.Dir(conf.CacheFile), "huaweipush.db"))
	}
	if err != nil {
		return nil, err
	}
}
```

Add them to the Server struct literal:

```go
huaweiPushClient: huaweiPushClient,
huaweiPushStore:  huaweiPushStore,
```

**Step 3: Add route** (find where `apiWebPushPath` is handled, add nearby)

Add path constant:

```go
apiHuaweiPushPath = "/v1/huaweipush"
```

Add routing (find the `apiWebPushPath` handler and add after it):

```go
} else if r.Method == http.MethodPost && r.URL.Path == apiHuaweiPushPath {
	return s.limitRequests(s.handleHuaweiPushUpdate)
} else if r.Method == http.MethodDelete && r.URL.Path == apiHuaweiPushPath {
	return s.limitRequests(s.handleHuaweiPushDelete)
```

**Step 4: Add publish trigger** (three locations in server.go)

Search for `s.publishToWebPushEndpoints` and add after each occurrence:

```go
if s.huaweiPushClient != nil {
	go s.sendToHuaweiPush(v, m)
}
```

There are three places:
1. ~line 890: initial publish
2. ~line 1012: update/delete message
3. ~line 2076: delayed message send

**Step 5: Add pruning to manager** (in `server/server_manager.go`)

After the `s.pruneAndNotifyWebPushSubscriptions()` call (~line 18):

```go
s.pruneHuaweiPushSubscriptions()
```

**Step 6: Verify it compiles**

Run: `go build ./...`
Expected: PASS

**Step 7: Commit**

```
git add server/server.go server/server_manager.go
git commit -m "feat(server): wire Huawei push into New(), routing, publish flow, and manager"
```

---

### Task 10: CLI flags

**Files:**
- Modify: `cmd/serve.go`

**Step 1: Add CLI flags** (in the `flagsServe` slice, after web-push flags ~line 111)

```go
altsrc.NewStringFlag(&cli.StringFlag{Name: "huawei-push-project-id", Aliases: []string{"huawei_push_project_id"}, EnvVars: []string{"NTFY_HUAWEI_PUSH_PROJECT_ID"}, Usage: "Huawei Push Kit project ID"}),
altsrc.NewStringFlag(&cli.StringFlag{Name: "huawei-push-client-id", Aliases: []string{"huawei_push_client_id"}, EnvVars: []string{"NTFY_HUAWEI_PUSH_CLIENT_ID"}, Usage: "Huawei Push Kit OAuth client ID"}),
altsrc.NewStringFlag(&cli.StringFlag{Name: "huawei-push-client-secret", Aliases: []string{"huawei_push_client_secret"}, EnvVars: []string{"NTFY_HUAWEI_PUSH_CLIENT_SECRET"}, Usage: "Huawei Push Kit OAuth client secret"}),
```

**Step 2: Read flags in execServe** (after the web-push reads ~line 154)

```go
huaweiPushProjectID := c.String("huawei-push-project-id")
huaweiPushClientID := c.String("huawei-push-client-id")
huaweiPushClientSecret := c.String("huawei-push-client-secret")
```

**Step 3: Add validation** (in the checks section, after WebPush checks ~line 346)

```go
} else if !server.HuaweiPushAvailable && (huaweiPushProjectID != "" || huaweiPushClientID != "" || huaweiPushClientSecret != "") {
	return errors.New("cannot enable Huawei Push, support is not available in this build (nohuaweipush)")
} else if (huaweiPushProjectID != "" || huaweiPushClientID != "" || huaweiPushClientSecret != "") && (huaweiPushProjectID == "" || huaweiPushClientID == "" || huaweiPushClientSecret == "") {
	return errors.New("if Huawei Push is enabled, huawei-push-project-id, huawei-push-client-id, and huawei-push-client-secret must all be set")
```

**Step 4: Set config** (in the config assignment section ~line 505)

```go
conf.HuaweiPushProjectID = huaweiPushProjectID
conf.HuaweiPushClientID = huaweiPushClientID
conf.HuaweiPushClientSecret = huaweiPushClientSecret
```

**Step 5: Verify it compiles**

Run: `go build ./...`
Expected: PASS

**Step 6: Commit**

```
git add cmd/serve.go
git commit -m "feat(cmd): add Huawei Push CLI flags and config loading"
```

---

### Task 11: Config file comments

**Files:**
- Modify: `server/server.yml`

**Step 1: Add Huawei Push config comments** (after the Firebase section)

```yaml
# If set, ntfy will forward push notifications to Huawei HarmonyOS devices via Huawei Push Kit V3.
# All three settings must be provided to enable Huawei Push. Obtain these from AppGallery Connect.
#
# huawei-push-project-id:
# huawei-push-client-id:
# huawei-push-client-secret:
```

**Step 2: Commit**

```
git add server/server.yml
git commit -m "docs: add Huawei Push config comments to server.yml"
```

---

### Task 12: End-to-end integration test

**Files:**
- Create: `server/server_huaweipush_test.go`

**Step 1: Write integration test**

This test verifies the full flow: register subscription via API → publish message → verify Huawei push was called → verify invalid token cleanup.

The test should follow the pattern in `server/server_test.go` using `newTestServer`, `newTestConfig`, `request()`, and `forEachBackend`.

Key test cases:
- `TestServer_HuaweiPush_PublishAndPush`: register token → publish → verify push server received the message
- `TestServer_HuaweiPush_RegisterDeleteSubscription`: register → delete → verify no push sent
- `TestServer_HuaweiPush_InvalidToken`: register → publish → mock returns invalid token → verify token removed from store

**Step 2: Run tests**

Run: `go test ./server/ -v -run TestServer_HuaweiPush`
Expected: All PASS

**Step 3: Commit**

```
git add server/server_huaweipush_test.go
git commit -m "test(server): add Huawei push integration tests"
```

---

### Task 13: Final verification

**Step 1: Run full test suite**

Run: `make test`
Expected: All PASS

**Step 2: Run linting**

Run: `make fmt-check && make vet`
Expected: PASS

**Step 3: Verify nohuaweipush build tag works**

Run: `go build -tags nohuaweipush ./...`
Expected: PASS

**Step 4: Verify normal build**

Run: `go build ./...`
Expected: PASS
