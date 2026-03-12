//go:build !nohuaweipush

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"heckel.io/ntfy/v2/huaweipush"
)

func newTestConfigWithHuaweiPush(t *testing.T, databaseURL string, tokenServerURL, pushServerURL string) *Config {
	conf := newTestConfig(t, databaseURL)
	conf.HuaweiPushProjectID = "test-project-id"
	conf.HuaweiPushClientID = "test-client-id"
	conf.HuaweiPushClientSecret = "test-client-secret"
	return conf
}

func newTestHuaweiPushServer(t *testing.T, conf *Config, tokenServerURL, pushServerURL string) *Server {
	s := newTestServer(t, conf)
	s.huaweiPushClient = huaweipush.NewClientWithURL(
		conf.HuaweiPushProjectID,
		conf.HuaweiPushClientID,
		conf.HuaweiPushClientSecret,
		tokenServerURL,
		pushServerURL,
	)
	if s.huaweiPushStore == nil {
		var err error
		s.huaweiPushStore, err = huaweipush.NewSQLiteStore(filepath.Join(t.TempDir(), "huaweipush-test.db"))
		require.Nil(t, err)
		t.Cleanup(func() { s.huaweiPushStore.Close() })
	}
	return s
}

func newMockHuaweiTokenServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "test-access-token", "expires_in": 3600})
	}))
}

func TestServer_HuaweiPush_Disabled(t *testing.T) {
	s := newTestServer(t, newTestConfig(t, ""))
	response := request(t, s, "POST", "/v1/huaweipush", `{"push_token":"token1","topics":["test-topic"]}`, nil)
	require.Equal(t, 404, response.Code)
}

func TestServer_HuaweiPush_Register(t *testing.T) {
	tokenServer := newMockHuaweiTokenServer(t)
	defer tokenServer.Close()
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": "80000000", "msg": "Success"})
	}))
	defer pushServer.Close()

	conf := newTestConfigWithHuaweiPush(t, "", tokenServer.URL, pushServer.URL)
	s := newTestHuaweiPushServer(t, conf, tokenServer.URL, pushServer.URL)

	// Register subscription
	response := request(t, s, "POST", "/v1/huaweipush", `{"push_token":"token1","topics":["test-topic","other-topic"]}`, nil)
	require.Equal(t, 200, response.Code)
	require.Equal(t, `{"success":true}`+"\n", response.Body.String())

	// Verify store
	tokens, err := s.huaweiPushStore.SubscriptionsForTopic("test-topic")
	require.Nil(t, err)
	require.Len(t, tokens, 1)
	require.Equal(t, "token1", tokens[0])

	tokens, err = s.huaweiPushStore.SubscriptionsForTopic("other-topic")
	require.Nil(t, err)
	require.Len(t, tokens, 1)
}

func TestServer_HuaweiPush_Register_InvalidRequest(t *testing.T) {
	tokenServer := newMockHuaweiTokenServer(t)
	defer tokenServer.Close()
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer pushServer.Close()

	conf := newTestConfigWithHuaweiPush(t, "", tokenServer.URL, pushServer.URL)
	s := newTestHuaweiPushServer(t, conf, tokenServer.URL, pushServer.URL)

	// Missing push_token
	response := request(t, s, "POST", "/v1/huaweipush", `{"topics":["test-topic"]}`, nil)
	require.Equal(t, 400, response.Code)

	// Empty body
	response = request(t, s, "POST", "/v1/huaweipush", `{}`, nil)
	require.Equal(t, 400, response.Code)

	// Invalid JSON
	response = request(t, s, "POST", "/v1/huaweipush", `not json`, nil)
	require.Equal(t, 400, response.Code)
}

func TestServer_HuaweiPush_Delete(t *testing.T) {
	tokenServer := newMockHuaweiTokenServer(t)
	defer tokenServer.Close()
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": "80000000", "msg": "Success"})
	}))
	defer pushServer.Close()

	conf := newTestConfigWithHuaweiPush(t, "", tokenServer.URL, pushServer.URL)
	s := newTestHuaweiPushServer(t, conf, tokenServer.URL, pushServer.URL)

	// Register then delete
	response := request(t, s, "POST", "/v1/huaweipush", `{"push_token":"token1","topics":["test-topic"]}`, nil)
	require.Equal(t, 200, response.Code)

	tokens, err := s.huaweiPushStore.SubscriptionsForTopic("test-topic")
	require.Nil(t, err)
	require.Len(t, tokens, 1)

	response = request(t, s, "DELETE", "/v1/huaweipush", `{"push_token":"token1"}`, nil)
	require.Equal(t, 200, response.Code)

	tokens, err = s.huaweiPushStore.SubscriptionsForTopic("test-topic")
	require.Nil(t, err)
	require.Len(t, tokens, 0)
}

func TestServer_HuaweiPush_PublishAndPush(t *testing.T) {
	tokenServer := newMockHuaweiTokenServer(t)
	defer tokenServer.Close()

	var received atomic.Bool
	var mu sync.Mutex
	var receivedBody map[string]any
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
		require.Equal(t, "0", r.Header.Get("push-type"))
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		receivedBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": "80000000", "msg": "Success"})
		received.Store(true)
	}))
	defer pushServer.Close()

	conf := newTestConfigWithHuaweiPush(t, "", tokenServer.URL, pushServer.URL)
	s := newTestHuaweiPushServer(t, conf, tokenServer.URL, pushServer.URL)

	// Register subscription
	response := request(t, s, "POST", "/v1/huaweipush", `{"push_token":"device-token-1","topics":["mytopic"]}`, nil)
	require.Equal(t, 200, response.Code)

	// Publish message
	response = request(t, s, "POST", "/mytopic", "Hello HarmonyOS", nil)
	require.Equal(t, 200, response.Code)

	// Wait for async push
	waitFor(t, func() bool {
		return received.Load()
	})

	// Verify push payload
	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, receivedBody)
	target, ok := receivedBody["target"].(map[string]any)
	require.True(t, ok)
	tokens, ok := target["token"].([]any)
	require.True(t, ok)
	require.Len(t, tokens, 1)
	require.Equal(t, "device-token-1", tokens[0])
}

func TestServer_HuaweiPush_PublishNoSubscribers(t *testing.T) {
	tokenServer := newMockHuaweiTokenServer(t)
	defer tokenServer.Close()

	var pushCalled atomic.Bool
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushCalled.Store(true)
		json.NewEncoder(w).Encode(map[string]any{"code": "80000000", "msg": "Success"})
	}))
	defer pushServer.Close()

	conf := newTestConfigWithHuaweiPush(t, "", tokenServer.URL, pushServer.URL)
	s := newTestHuaweiPushServer(t, conf, tokenServer.URL, pushServer.URL)

	// Publish without any subscribers
	response := request(t, s, "POST", "/mytopic", "no one listening", nil)
	require.Equal(t, 200, response.Code)

	// Push server should not be called (no subscribers)
	// Give it a moment to ensure it doesn't fire
	require.False(t, pushCalled.Load())
}

func TestServer_HuaweiPush_UpdateTopics(t *testing.T) {
	tokenServer := newMockHuaweiTokenServer(t)
	defer tokenServer.Close()
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": "80000000", "msg": "Success"})
	}))
	defer pushServer.Close()

	conf := newTestConfigWithHuaweiPush(t, "", tokenServer.URL, pushServer.URL)
	s := newTestHuaweiPushServer(t, conf, tokenServer.URL, pushServer.URL)

	// Register with two topics
	response := request(t, s, "POST", "/v1/huaweipush", `{"push_token":"token1","topics":["topicA","topicB"]}`, nil)
	require.Equal(t, 200, response.Code)

	tokens, err := s.huaweiPushStore.SubscriptionsForTopic("topicB")
	require.Nil(t, err)
	require.Len(t, tokens, 1)

	// Update to only one topic (upsert replaces)
	response = request(t, s, "POST", "/v1/huaweipush", `{"push_token":"token1","topics":["topicA"]}`, nil)
	require.Equal(t, 200, response.Code)

	tokens, err = s.huaweiPushStore.SubscriptionsForTopic("topicB")
	require.Nil(t, err)
	require.Len(t, tokens, 0)

	tokens, err = s.huaweiPushStore.SubscriptionsForTopic("topicA")
	require.Nil(t, err)
	require.Len(t, tokens, 1)
}
