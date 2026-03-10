package huaweipush_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"heckel.io/ntfy/v2/huaweipush"
)

func TestClient_Send_Success(t *testing.T) {
	var tokenRequests int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenRequests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"test-token-123","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	var pushRequests int32
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushRequests, 1)
		require.Equal(t, "Bearer test-token-123", r.Header.Get("Authorization"))
		require.Equal(t, "0", r.Header.Get("push-type"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":"80000000","msg":"Success"}`)
	}))
	defer pushServer.Close()

	client := huaweipush.NewClientWithURL("proj1", "client1", "secret1", tokenServer.URL, pushServer.URL)
	payload := json.RawMessage(`{"key":"value"}`)
	result, err := client.Send([]string{"token-a", "token-b"}, 0, payload)
	require.NoError(t, err)
	require.Equal(t, 2, result.SuccessCount)
	require.Equal(t, 0, result.FailureCount)
}

func TestClient_Send_EmptyTokens(t *testing.T) {
	client := huaweipush.NewClient("proj1", "client1", "secret1")
	result, err := client.Send([]string{}, 0, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Equal(t, 0, result.SuccessCount)
	require.Equal(t, 0, result.FailureCount)
}

func TestClient_Send_TokenRefreshCached(t *testing.T) {
	var tokenRequests int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenRequests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"cached-token","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":"80000000","msg":"Success"}`)
	}))
	defer pushServer.Close()

	client := huaweipush.NewClientWithURL("proj1", "client1", "secret1", tokenServer.URL, pushServer.URL)
	payload := json.RawMessage(`{}`)

	_, err := client.Send([]string{"tok1"}, 0, payload)
	require.NoError(t, err)

	_, err = client.Send([]string{"tok2"}, 0, payload)
	require.NoError(t, err)

	require.Equal(t, int32(1), atomic.LoadInt32(&tokenRequests))
}

func TestClient_Send_Batching(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"batch-token","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	var pushRequests int32
	var batchSizes []int
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushRequests, 1)
		var body struct {
			Target struct {
				Token []string `json:"token"`
			} `json:"target"`
		}
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		batchSizes = append(batchSizes, len(body.Target.Token))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":"80000000","msg":"Success"}`)
	}))
	defer pushServer.Close()

	client := huaweipush.NewClientWithURL("proj1", "client1", "secret1", tokenServer.URL, pushServer.URL)

	tokens := make([]string, 2500)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("token-%d", i)
	}

	result, err := client.Send(tokens, 0, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Equal(t, int32(3), atomic.LoadInt32(&pushRequests))
	require.Equal(t, 2500, result.SuccessCount)
	require.Equal(t, 0, result.FailureCount)
	require.Len(t, batchSizes, 3)
	require.Equal(t, 1000, batchSizes[0])
	require.Equal(t, 1000, batchSizes[1])
	require.Equal(t, 500, batchSizes[2])
}
