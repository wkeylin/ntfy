package huaweipush

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
