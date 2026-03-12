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

// Client is a Huawei Push Kit client that handles OAuth2 token management and batch push sending.
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

// NewClient creates a new Huawei Push Kit client with the default token and push URLs.
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

// NewClientWithURL creates a new Huawei Push Kit client with custom token and push URLs.
func NewClientWithURL(projectID, clientID, clientSecret, tokenURL, pushURL string) *Client {
	c := NewClient(projectID, clientID, clientSecret)
	c.tokenURL = tokenURL
	c.pushURL = pushURL
	return c
}

type huaweiPushRequest struct {
	Payload json.RawMessage  `json:"payload"`
	Target  huaweiPushTarget `json:"target"`
}

type huaweiPushTarget struct {
	Token []string `json:"token"`
}

type huaweiPushResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

type huaweiTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// Send sends a push message to the given tokens, splitting into batches of huaweiPushTokenBatchSize.
// It returns the aggregated result across all batches.
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
		batchResult, err := c.sendBatch(batch, pushType, payload)
		if err != nil {
			return nil, err
		}
		result.SuccessCount += batchResult.SuccessCount
		result.FailureCount += batchResult.FailureCount
		result.InvalidTokens = append(result.InvalidTokens, batchResult.InvalidTokens...)
	}
	return result, nil
}

func (c *Client) sendBatch(tokens []string, pushType int, payload json.RawMessage) (*SendResult, error) {
	token, err := c.getAccessToken()
	if err != nil {
		return nil, err
	}
	reqBody := huaweiPushRequest{
		Payload: payload,
		Target: huaweiPushTarget{
			Token: tokens,
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errHuaweiPushFailed, err)
	}
	req, err := http.NewRequest(http.MethodPost, c.pushURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errHuaweiPushFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("push-type", fmt.Sprintf("%d", pushType))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errHuaweiPushFailed, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errHuaweiPushFailed, err)
	}
	var pushResp huaweiPushResponse
	if err := json.Unmarshal(respBody, &pushResp); err != nil {
		return nil, fmt.Errorf("%w: %s", errHuaweiPushFailed, err)
	}
	result := &SendResult{}
	if pushResp.Code == "80000000" {
		result.SuccessCount = len(tokens)
	} else {
		result.FailureCount = len(tokens)
	}
	return result, nil
}

func (c *Client) getAccessToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Now().Add(5*time.Minute).Before(c.tokenExpiry) {
		return c.accessToken, nil
	}
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", c.clientID)
	data.Set("client_secret", c.clientSecret)
	resp, err := c.httpClient.PostForm(c.tokenURL, data)
	if err != nil {
		return "", fmt.Errorf("%w: %s", errHuaweiAccessTokenFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%w: %s", errHuaweiAccessTokenFailed, err)
	}
	var tokenResp huaweiTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("%w: %s", errHuaweiAccessTokenFailed, err)
	}
	if tokenResp.AccessToken == "" {
		return "", errHuaweiAccessTokenFailed
	}
	c.accessToken = tokenResp.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return c.accessToken, nil
}
