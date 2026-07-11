package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pushlib "github.com/SherClockHolmes/webpush-go"
)

const (
	DefaultSendTimeout = 10 * time.Second
	DefaultTTL         = 300
	maximumPayloadSize = 3000
)

// Payload is intentionally compact and contains no capability material.
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

type Status struct {
	Enabled           bool   `json:"enabled"`
	PublicKey         string `json:"public_key"`
	SubscriptionCount int    `json:"subscription_count"`
}

type SendResult struct {
	Attempted int      `json:"attempted"`
	Delivered int      `json:"delivered"`
	Removed   int      `json:"removed"`
	Errors    []string `json:"errors,omitempty"`
}

func (r SendResult) Err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	return errors.New(strings.Join(r.Errors, "; "))
}

type SendOptions struct {
	TTL     int
	Topic   string
	Urgency pushlib.Urgency
}

type Sender interface {
	SendNotificationWithContext(context.Context, []byte, *pushlib.Subscription, *pushlib.Options) (*http.Response, error)
}

type librarySender struct{}

func (librarySender) SendNotificationWithContext(ctx context.Context, payload []byte, subscription *pushlib.Subscription, options *pushlib.Options) (*http.Response, error) {
	return pushlib.SendNotificationWithContext(ctx, payload, subscription, options)
}

type Service struct {
	repository Repository
	sender     Sender
	client     pushlib.HTTPClient
	subscriber string
	timeout    time.Duration
}

func NewService(repository Repository, subscriber string, client *http.Client) (*Service, error) {
	if repository == nil {
		return nil, errors.New("web push repository is required")
	}
	subscriber = strings.TrimSpace(subscriber)
	if subscriber == "" {
		return nil, errors.New("web push VAPID subscriber is required")
	}
	if client == nil {
		client = &http.Client{Timeout: DefaultSendTimeout}
	}
	return &Service{repository: repository, sender: librarySender{}, client: client, subscriber: subscriber, timeout: DefaultSendTimeout}, nil
}

// NewServiceWithSender is intended for focused delivery tests and adapters.
func NewServiceWithSender(repository Repository, subscriber string, sender Sender) (*Service, error) {
	service, err := NewService(repository, subscriber, nil)
	if err != nil {
		return nil, err
	}
	if sender == nil {
		return nil, errors.New("web push sender is required")
	}
	service.sender = sender
	return service, nil
}

func (s *Service) PublicKey(ctx context.Context) (string, error) {
	pair, err := s.repository.EnsureVAPIDKeyPair(ctx)
	if err != nil {
		return "", err
	}
	return pair.PublicKey, nil
}

func (s *Service) Status(ctx context.Context, accountScopeID string) (Status, error) {
	pair, err := s.repository.EnsureVAPIDKeyPair(ctx)
	if err != nil {
		return Status{}, err
	}
	records, err := s.repository.ListSubscriptions(ctx, accountScopeID, maximumListLimit)
	if err != nil {
		return Status{}, err
	}
	return Status{Enabled: len(records) > 0, PublicKey: pair.PublicKey, SubscriptionCount: len(records)}, nil
}

func (s *Service) Upsert(ctx context.Context, accountScopeID string, input SubscriptionInput) (Subscription, bool, error) {
	return s.repository.UpsertSubscription(ctx, accountScopeID, input)
}

func (s *Service) List(ctx context.Context, accountScopeID string, limit int) ([]Subscription, error) {
	return s.repository.ListSubscriptions(ctx, accountScopeID, limit)
}

func (s *Service) Delete(ctx context.Context, accountScopeID, subscriptionID string) (bool, error) {
	return s.repository.DeleteSubscription(ctx, accountScopeID, subscriptionID)
}

func (s *Service) Test(ctx context.Context, accountScopeID string) (SendResult, error) {
	return s.Send(ctx, accountScopeID, Payload{Title: "Swarm notifications enabled", Body: "Web Push delivery is working.", Tag: "swarm-webpush-test"}, SendOptions{Topic: "swarm-webpush-test", TTL: DefaultTTL})
}

func (s *Service) Send(ctx context.Context, accountScopeID string, payload Payload, options SendOptions) (SendResult, error) {
	if err := validatePayload(payload); err != nil {
		return SendResult{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return SendResult{}, fmt.Errorf("encode web push payload: %w", err)
	}
	if len(encoded) > maximumPayloadSize {
		return SendResult{}, fmt.Errorf("web push payload is %d bytes; maximum is %d", len(encoded), maximumPayloadSize)
	}
	pair, err := s.repository.EnsureVAPIDKeyPair(ctx)
	if err != nil {
		return SendResult{}, err
	}
	records, err := s.repository.ListStoredSubscriptions(ctx, accountScopeID, maximumListLimit)
	if err != nil {
		return SendResult{}, err
	}
	result := SendResult{Attempted: len(records)}
	for _, record := range records {
		if err := contextErr(ctx); err != nil {
			result.Errors = append(result.Errors, err.Error())
			return result, result.Err()
		}
		sendCtx, cancel := boundedContext(ctx, s.timeout)
		response, sendErr := s.sender.SendNotificationWithContext(sendCtx, encoded, &pushlib.Subscription{
			Endpoint: record.Endpoint,
			Keys:     pushlib.Keys{Auth: record.Auth, P256dh: record.P256DH},
		}, &pushlib.Options{
			HTTPClient: s.client, Subscriber: s.subscriber,
			VAPIDPublicKey: pair.PublicKey, VAPIDPrivateKey: pair.PrivateKey,
			TTL: normalizedTTL(options.TTL), Topic: strings.TrimSpace(options.Topic), Urgency: options.Urgency,
		})
		cancel()
		if sendErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("subscription %s: %v", record.ID, sendErr))
			continue
		}
		statusCode, bodyErr := consumeAndClose(response)
		if bodyErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("subscription %s: close response: %v", record.ID, bodyErr))
		}
		switch {
		case statusCode >= 200 && statusCode < 300:
			result.Delivered++
		case statusCode == http.StatusNotFound || statusCode == http.StatusGone:
			removed, deleteErr := s.repository.DeleteSubscription(ctx, accountScopeID, record.ID)
			if deleteErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("subscription %s expired; cleanup failed: %v", record.ID, deleteErr))
			} else if removed {
				result.Removed++
			}
		default:
			result.Errors = append(result.Errors, fmt.Sprintf("subscription %s: push service returned HTTP %d", record.ID, statusCode))
		}
	}
	return result, result.Err()
}

func validatePayload(payload Payload) error {
	payload.Title = strings.TrimSpace(payload.Title)
	if payload.Title == "" {
		return errors.New("web push payload title is required")
	}
	if strings.ContainsAny(payload.URL, "\r\n") {
		return errors.New("web push payload URL contains control characters")
	}
	return nil
}

func normalizedTTL(ttl int) int {
	if ttl <= 0 {
		return DefaultTTL
	}
	const maxTTL = 28 * 24 * 60 * 60
	if ttl > maxTTL {
		return maxTTL
	}
	return ttl
}

func boundedContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		timeout = DefaultSendTimeout
	}
	return context.WithTimeout(parent, timeout)
}

func consumeAndClose(response *http.Response) (int, error) {
	if response == nil {
		return 0, errors.New("web push sender returned no response")
	}
	if response.Body == nil {
		return response.StatusCode, nil
	}
	_, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	closeErr := response.Body.Close()
	return response.StatusCode, errors.Join(copyErr, closeErr)
}
