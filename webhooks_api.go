package walletd

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// CreateWebhookEndpoint registers a receiver.
//
// The signing secret is in WebhookEndpoint.Secret and is returned exactly
// once, here. Store it in your secret manager before this function's return
// value goes out of scope; it cannot be read back.
func (c *Client) CreateWebhookEndpoint(ctx context.Context, req CreateWebhookEndpointRequest) (WebhookEndpoint, error) {
	var out WebhookEndpoint
	err := c.do(ctx, http.MethodPost, "/v1/webhook_endpoints", "", nil, req, &out)
	return out, err
}

// ListWebhookEndpoints returns the tenant's endpoints, without secrets.
func (c *Client) ListWebhookEndpoints(ctx context.Context) ([]WebhookEndpoint, error) {
	var out []WebhookEndpoint
	err := c.do(ctx, http.MethodGet, "/v1/webhook_endpoints", "", nil, nil, &out)
	return out, err
}

// ListWebhookEvents returns recent events, newest first. History is kept
// for 30 days.
//
// The contract gives this listing a since filter and a limit but no cursor,
// so it cannot be paged: narrow the window instead of asking for more.
func (c *Client) ListWebhookEvents(ctx context.Context, eventType string, since time.Time, limit int) ([]WebhookEvent, error) {
	q := url.Values{}
	if eventType != "" {
		q.Set("type", eventType)
	}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339Nano))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []WebhookEvent
	err := c.do(ctx, http.MethodGet, "/v1/webhook_events", "", q, nil, &out)
	return out, err
}

// RedeliverWebhookEvent re-queues one event to every endpoint that should
// receive it, and returns how many deliveries were enqueued. Use it when a
// receiver was down past its retry window.
func (c *Client) RedeliverWebhookEvent(ctx context.Context, eventID uuid.UUID) (int, error) {
	var out struct {
		Enqueued int `json:"enqueued"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/webhook_events/"+eventID.String()+"/redeliver", "", nil, nil, &out)
	return out.Enqueued, err
}

// ListWebhookDeliveries reads one page of delivery attempts. Prefer
// WebhookDeliveries.
func (c *Client) ListWebhookDeliveries(ctx context.Context, endpointID *uuid.UUID, cursor uuid.UUID, limit int) ([]WebhookDelivery, error) {
	q := pageQuery(cursor, limit)
	if endpointID != nil {
		q.Set("endpoint_id", endpointID.String())
	}
	var out []WebhookDelivery
	err := c.do(ctx, http.MethodGet, "/v1/webhook_deliveries", "", q, nil, &out)
	return out, err
}

// WebhookDeliveries iterates delivery attempts, newest first.
func (c *Client) WebhookDeliveries(endpointID *uuid.UUID) *Iterator[WebhookDelivery] {
	return newIterator(
		func(ctx context.Context, cursor uuid.UUID, limit int) ([]WebhookDelivery, error) {
			return c.ListWebhookDeliveries(ctx, endpointID, cursor, limit)
		},
		func(d WebhookDelivery) uuid.UUID { return d.Id },
	)
}
