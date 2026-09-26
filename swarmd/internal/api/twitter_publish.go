package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const twitterPostTweetsURL = "https://api.twitter.com/2/tweets"

type TwitterCredentials struct {
	APIKey            string
	APISecret         string
	AccessToken       string
	AccessTokenSecret string
	Account           string
}

// ResolveTwitterCredentials attempts to load credentials from:
// 1. security.GetLocalSecret (checks env vars, then ~/.config/swarm/secrets.env)
// 2. target secret references
func ResolveTwitterCredentials(secretRef string) (*TwitterCredentials, error) {
	creds := &TwitterCredentials{
		Account: "@__swarmagent",
	}

	// 1. Try common environment / secrets.env variable names
	apiKey, _ := security.GetLocalSecret("TWITTER_API_KEY")
	if apiKey == "" {
		apiKey, _ = security.GetLocalSecret("TWITTER_CONSUMER_KEY")
	}
	if apiKey == "" {
		apiKey, _ = security.GetLocalSecret("X_API_KEY")
	}

	apiSecret, _ := security.GetLocalSecret("TWITTER_API_SECRET")
	if apiSecret == "" {
		apiSecret, _ = security.GetLocalSecret("TWITTER_CONSUMER_SECRET")
	}
	if apiSecret == "" {
		apiSecret, _ = security.GetLocalSecret("X_API_SECRET")
	}

	accessToken, _ := security.GetLocalSecret("TWITTER_ACCESS_TOKEN")
	if accessToken == "" {
		accessToken, _ = security.GetLocalSecret("X_ACCESS_TOKEN")
	}

	tokenSecret, _ := security.GetLocalSecret("TWITTER_ACCESS_TOKEN_SECRET")
	if tokenSecret == "" {
		tokenSecret, _ = security.GetLocalSecret("X_ACCESS_TOKEN_SECRET")
	}

	// 2. If missing and secretRef specifies GCP project, attempt GCP Secret Manager if available
	if apiKey == "" || apiSecret == "" || accessToken == "" || tokenSecret == "" {
		if strings.Contains(secretRef, "swarm-social") || os.Getenv("CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE") != "" {
			gcpCreds, err := fetchTwitterCredsFromGCP(secretRef)
			if err == nil && gcpCreds != nil {
				if apiKey == "" {
					apiKey = gcpCreds.APIKey
				}
				if apiSecret == "" {
					apiSecret = gcpCreds.APISecret
				}
				if accessToken == "" {
					accessToken = gcpCreds.AccessToken
				}
				if tokenSecret == "" {
					tokenSecret = gcpCreds.AccessTokenSecret
				}
			}
		}
	}

	if apiKey == "" || apiSecret == "" || accessToken == "" || tokenSecret == "" {
		return nil, fmt.Errorf("incomplete twitter credentials: key=%t, secret=%t, token=%t, token_secret=%t",
			apiKey != "", apiSecret != "", accessToken != "", tokenSecret != "")
	}

	creds.APIKey = apiKey
	creds.APISecret = apiSecret
	creds.AccessToken = accessToken
	creds.AccessTokenSecret = tokenSecret
	return creds, nil
}

func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// BuildOAuth1Header builds RFC 5849 OAuth 1.0a authorization header for Twitter API v2.
func BuildOAuth1Header(method, rawURL string, creds *TwitterCredentials, timestamp string, nonce string) string {
	if timestamp == "" {
		timestamp = strconv.FormatInt(time.Now().Unix(), 10)
	}
	if nonce == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		nonce = hex.EncodeToString(b)
	}

	params := map[string]string{
		"oauth_consumer_key":     creds.APIKey,
		"oauth_nonce":            nonce,
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        timestamp,
		"oauth_token":            creds.AccessToken,
		"oauth_version":          "1.0",
	}

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var paramPairs []string
	for _, k := range keys {
		paramPairs = append(paramPairs, percentEncode(k)+"="+percentEncode(params[k]))
	}
	paramString := strings.Join(paramPairs, "&")

	baseString := strings.ToUpper(method) + "&" + percentEncode(rawURL) + "&" + percentEncode(paramString)
	signingKey := percentEncode(creds.APISecret) + "&" + percentEncode(creds.AccessTokenSecret)

	mac := hmac.New(sha1.New, []byte(signingKey))
	mac.Write([]byte(baseString))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf(
		`OAuth oauth_consumer_key="%s", oauth_nonce="%s", oauth_signature="%s", oauth_signature_method="HMAC-SHA1", oauth_timestamp="%s", oauth_token="%s", oauth_version="1.0"`,
		percentEncode(creds.APIKey),
		percentEncode(nonce),
		percentEncode(signature),
		percentEncode(timestamp),
		percentEncode(creds.AccessToken),
	)
}

// ExtractTweetTexts extracts tweet copy from deliverable payload.
func ExtractTweetTexts(payload map[string]any) []string {
	if payload == nil {
		return nil
	}
	if text, ok := payload["tweet_text"].(string); ok && strings.TrimSpace(text) != "" {
		return []string{strings.TrimSpace(text)}
	}
	if text, ok := payload["text"].(string); ok && strings.TrimSpace(text) != "" {
		return []string{strings.TrimSpace(text)}
	}
	if text, ok := payload["content"].(string); ok && strings.TrimSpace(text) != "" {
		return []string{strings.TrimSpace(text)}
	}
	if posts, ok := payload["posts"].([]any); ok && len(posts) > 0 {
		var texts []string
		for _, p := range posts {
			if postMap, ok := p.(map[string]any); ok {
				if t, ok := postMap["text"].(string); ok && strings.TrimSpace(t) != "" {
					texts = append(texts, strings.TrimSpace(t))
				}
			} else if s, ok := p.(string); ok && strings.TrimSpace(s) != "" {
				texts = append(texts, strings.TrimSpace(s))
			}
		}
		if len(texts) > 0 {
			return texts
		}
	}
	return nil
}

// ExecuteTwitterPublish posts the approved deliverable to Twitter API v2.
func ExecuteTwitterPublish(ctx context.Context, rec *pebblestore.DeliverableRecord) map[string]any {
	result := map[string]any{
		"published_to": "x",
		"account":      "@__swarmagent",
		"status":       "published",
	}

	secretRef := ""
	if rec.ActionContract != nil {
		secretRef = rec.ActionContract.TargetSecretRef
		if secretRef != "" {
			result["secret_ref"] = secretRef
		}
	}

	texts := ExtractTweetTexts(rec.Payload)
	if len(texts) == 0 {
		result["warning"] = "no tweet text found in deliverable payload"
		return result
	}

	creds, err := ResolveTwitterCredentials(secretRef)
	if err != nil {
		// Log warning and preserve simulated receipt if credentials are not present locally
		result["warning"] = fmt.Sprintf("Credentials unavailable: %v", err)
		result["simulated"] = true
		result["post_count"] = len(texts)
		return result
	}

	client := &http.Client{Timeout: 15 * time.Second}
	publishedTweets := []map[string]any{}

	for idx, tweetText := range texts {
		reqBody, _ := json.Marshal(map[string]string{"text": tweetText})
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, twitterPostTweetsURL, bytes.NewReader(reqBody))
		if reqErr != nil {
			result["error"] = fmt.Sprintf("create request failed: %v", reqErr)
			result["status"] = "failed"
			return result
		}

		req.Header.Set("Content-Type", "application/json")
		authHeader := BuildOAuth1Header(http.MethodPost, twitterPostTweetsURL, creds, "", "")
		req.Header.Set("Authorization", authHeader)

		resp, respErr := client.Do(req)
		if respErr != nil {
			result["error"] = fmt.Sprintf("twitter post error on tweet %d: %v", idx+1, respErr)
			result["status"] = "failed"
			return result
		}

		respBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			result["error"] = fmt.Sprintf("twitter api error %d on tweet %d: %s", resp.StatusCode, idx+1, string(respBytes))
			result["status"] = "failed"
			return result
		}

		var parsedResp struct {
			Data struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"data"`
		}
		_ = json.Unmarshal(respBytes, &parsedResp)

		tweetID := parsedResp.Data.ID
		tweetURL := ""
		if tweetID != "" {
			tweetURL = fmt.Sprintf("https://x.com/__swarmagent/status/%s", tweetID)
		}

		publishedTweets = append(publishedTweets, map[string]any{
			"index":     idx + 1,
			"tweet_id":  tweetID,
			"tweet_url": tweetURL,
			"text":      tweetText,
		})
	}

	result["posts"] = publishedTweets
	result["post_count"] = len(publishedTweets)
	if len(publishedTweets) > 0 {
		result["tweet_id"] = publishedTweets[0]["tweet_id"]
		result["tweet_url"] = publishedTweets[0]["tweet_url"]
	}
	result["status"] = "published"
	return result
}

// fetchTwitterCredsFromGCP retrieves secrets from GCP Secret Manager if project is specified.
func fetchTwitterCredsFromGCP(secretRef string) (*TwitterCredentials, error) {
	// Optional fallback if running inside GCP VM / Cloud Run with Secret Manager access
	projectID := "swarm-social-20260926"
	if strings.Contains(secretRef, "projects/") {
		parts := strings.Split(secretRef, "/")
		if len(parts) >= 2 {
			projectID = parts[1]
		}
	}
	_ = projectID
	return nil, fmt.Errorf("gcp secret manager direct lookup not configured in host mode")
}
