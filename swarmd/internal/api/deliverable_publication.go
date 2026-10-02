package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func approveDeliverablePublication(ctx context.Context, db *pebblestore.SessionStore, account, id, reviewer string) (pebblestore.DeliverableRecord, error) {
	client := &http.Client{Timeout: 15*time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return approveDeliverablePublicationWithClient(ctx, db, account, id, reviewer, client)
}

func approveDeliverablePublicationWithClient(ctx context.Context, db *pebblestore.SessionStore, account, id, reviewer string, client *http.Client) (pebblestore.DeliverableRecord, error) {
	rec, claimed, err := db.ClaimDeliverablePublication(account, id, reviewer)
	if err != nil || !claimed { return rec, err }
	var result map[string]any
	if rec.ActionContract.Action == "publish_x_post" {
		result = executeTwitterPublish(ctx, &rec, client, func(receipt map[string]any) error {
			_, err := db.SaveDeliverablePublication(account, id, rec.PublicationClaim, "publishing", receipt)
			return err
		})
	} else {
		result = executeDeliverableWebhook(ctx, &rec, client)
	}
	status, _ := result["status"].(string)
	return db.SaveDeliverablePublication(account, id, rec.PublicationClaim, status, result)
}

func executeDeliverableWebhook(ctx context.Context, rec *pebblestore.DeliverableRecord, client *http.Client) map[string]any {
	result := map[string]any{"status": "publication_failed"}
	u, err := url.Parse(rec.ActionContract.TargetURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil { result["error"] = "valid HTTPS webhook required"; return result }
	// Do not export secret references or the publication fencing token.
	body, err := json.Marshal(map[string]any{"event": "deliverable.approved", "deliverable": map[string]any{"id": rec.ID, "title": rec.Title, "payload": rec.Payload}})
	if err != nil { result["error"] = "invalid webhook payload"; return result }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil { result["error"] = "invalid webhook request"; return result }
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil { result["status"] = "reconciliation_required"; result["error"] = "webhook outcome unknown; reconcile before retry"; return result }
	resp.Body.Close()
	result["webhook_status"] = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 { result["status"] = "published" } else {
		result["error"] = "webhook rejected publication"
		if resp.StatusCode >= 500 { result["status"] = "reconciliation_required" }
	}
	return result
}
