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
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const twitterPostTweetsURL = "https://api.twitter.com/2/tweets"

var twitterAccountPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
var twitterIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

type TwitterCredentials struct {
	APIKey string
	APISecret string
	AccessToken string
	AccessTokenSecret string
	Account string
}

// ResolveTwitterCredentials accepts only explicit env:TWITTER configuration.
// Operators inject TWITTER_API_KEY, TWITTER_API_SECRET, TWITTER_ACCESS_TOKEN,
// TWITTER_ACCESS_TOKEN_SECRET and TWITTER_ACCOUNT into the daemon environment.
// TWITTER_ACCOUNT_SCOPE_ID binds those credentials to one authenticated Swarm
// account. No home-file lookup, cloud lookup or implicit account is performed.
func ResolveTwitterCredentials(secretRef string) (*TwitterCredentials, error) {
	if secretRef != "env:TWITTER" { return nil, fmt.Errorf("explicit env:TWITTER credentials required") }
	c := &TwitterCredentials{APIKey: os.Getenv("TWITTER_API_KEY"), APISecret: os.Getenv("TWITTER_API_SECRET"), AccessToken: os.Getenv("TWITTER_ACCESS_TOKEN"), AccessTokenSecret: os.Getenv("TWITTER_ACCESS_TOKEN_SECRET"), Account: strings.TrimPrefix(os.Getenv("TWITTER_ACCOUNT"), "@")}
	if strings.TrimSpace(c.APIKey) == "" || strings.TrimSpace(c.APISecret) == "" || strings.TrimSpace(c.AccessToken) == "" || strings.TrimSpace(c.AccessTokenSecret) == "" || !twitterAccountPattern.MatchString(c.Account) { return nil, fmt.Errorf("incomplete Twitter credentials or account") }
	return c, nil
}

func percentEncode(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// BuildOAuth1Header builds RFC 5849 OAuth 1.0a authorization for Twitter API v2.
func BuildOAuth1Header(method, rawURL string, creds *TwitterCredentials, timestamp, nonce string) string {
	if timestamp == "" { timestamp = strconv.FormatInt(time.Now().Unix(), 10) }
	if nonce == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil { return "" }
		nonce = hex.EncodeToString(b)
	}
	params := map[string]string{"oauth_consumer_key": creds.APIKey, "oauth_nonce": nonce, "oauth_signature_method": "HMAC-SHA1", "oauth_timestamp": timestamp, "oauth_token": creds.AccessToken, "oauth_version": "1.0"}
	keys := make([]string, 0, len(params))
	for k := range params { keys = append(keys, k) }
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys { pairs = append(pairs, percentEncode(k)+"="+percentEncode(params[k])) }
	base := strings.ToUpper(method)+"&"+percentEncode(rawURL)+"&"+percentEncode(strings.Join(pairs, "&"))
	mac := hmac.New(sha1.New, []byte(percentEncode(creds.APISecret)+"&"+percentEncode(creds.AccessTokenSecret)))
	mac.Write([]byte(base))
	params["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	keys = append(keys, "oauth_signature")
	sort.Strings(keys)
	pairs = pairs[:0]
	for _, k := range keys { pairs = append(pairs, percentEncode(k)+`="`+percentEncode(params[k])+`"`) }
	return "OAuth "+strings.Join(pairs, ", ")
}

// ExtractTweetTexts rejects an entire batch if any entry is empty or malformed.
func ExtractTweetTexts(payload map[string]any) []string {
	for _, key := range []string{"tweet_text", "text", "content"} {
		if v, exists := payload[key]; exists {
			text, ok := v.(string)
			if !ok || strings.TrimSpace(text) == "" { return nil }
			return []string{strings.TrimSpace(text)}
		}
	}
	posts, ok := payload["posts"].([]any)
	if !ok { return nil }
	var texts []string
	for _, post := range posts {
		text, ok := post.(string)
		if m, isMap := post.(map[string]any); isMap { text, ok = m["text"].(string) }
		if !ok || strings.TrimSpace(text) == "" { return nil }
		texts = append(texts, strings.TrimSpace(text))
	}
	return texts
}

// ExecuteTwitterPublish deliberately refuses unclaimed external publication.
// Approval uses executeTwitterPublish with a synchronous durable receipt sink.
func ExecuteTwitterPublish(ctx context.Context, rec *pebblestore.DeliverableRecord) map[string]any {
	return map[string]any{"published_to": "x", "status": "publication_failed", "error": "durable approval claim required"}
}

func executeTwitterPublish(ctx context.Context, rec *pebblestore.DeliverableRecord, client *http.Client, save func(map[string]any) error) map[string]any {
	result := map[string]any{"published_to": "x", "status": "publication_failed"}
	fail := func(status, message string) map[string]any {
		if count, _ := result["post_count"].(int); count > 0 { status = "reconciliation_required" }
		result["status"] = status
		result["error"] = message
		return result
	}
	if rec == nil || rec.PublicationClaim == "" || rec.ActionContract == nil || save == nil { return fail("publication_failed", "durable approval claim required") }
	texts := ExtractTweetTexts(rec.Payload)
	if len(texts) == 0 { return fail("publication_failed", "nonempty post text required") }
	if rec.AccountID == "" || os.Getenv("TWITTER_ACCOUNT_SCOPE_ID") != rec.AccountID { return fail("publication_failed", "Twitter credentials not configured for this account") }
	creds, err := ResolveTwitterCredentials(rec.ActionContract.TargetSecretRef)
	if err != nil { return fail("publication_failed", "Twitter credentials unavailable") }
	result["account"] = creds.Account
	posts := []map[string]any{}
	for i, text := range texts {
		body, _ := json.Marshal(map[string]string{"text": text})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, twitterPostTweetsURL, bytes.NewReader(body))
		if err != nil { return fail("publication_failed", "invalid publication request") }
		req.Header.Set("Content-Type", "application/json")
		auth := BuildOAuth1Header(http.MethodPost, twitterPostTweetsURL, creds, "", "")
		if auth == "" { return fail("publication_failed", "unable to sign publication") }
		req.Header.Set("Authorization", auth)
		resp, err := client.Do(req)
		if err != nil { return fail("reconciliation_required", "publication outcome unknown; reconcile before retry") }
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			status := "publication_failed"
			if len(posts) > 0 || resp.StatusCode >= 500 { status = "reconciliation_required" }
			return fail(status, "Twitter rejected publication")
		}
		var receipt struct { Data struct { ID string `json:"id"` } `json:"data"` }
		if readErr != nil || len(data) > 65536 || json.Unmarshal(data, &receipt) != nil || !twitterIDPattern.MatchString(receipt.Data.ID) { return fail("reconciliation_required", "invalid Twitter receipt; reconcile before retry") }
		if _, err := strconv.ParseUint(receipt.Data.ID, 10, 64); err != nil { return fail("reconciliation_required", "invalid Twitter post ID") }
		posts = append(posts, map[string]any{"index": i+1, "tweet_id": receipt.Data.ID, "tweet_url": "https://x.com/"+creds.Account+"/status/"+receipt.Data.ID})
		result["posts"], result["post_count"] = posts, len(posts)
		result["tweet_id"], result["tweet_url"] = posts[0]["tweet_id"], posts[0]["tweet_url"]
		result["status"] = "reconciliation_required"
		if err := save(result); err != nil { return fail("reconciliation_required", "receipt persistence failed; reconcile before retry") }
	}
	result["status"] = "published"
	return result
}
