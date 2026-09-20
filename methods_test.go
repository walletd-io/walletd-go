package walletd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	userID         = uuid.MustParse("019fee0b-b5d9-7dd6-945f-337ab9dd09bc")
	otherUserID    = uuid.MustParse("019fee0b-b5d9-7dd6-945f-337ab9dd0aaa")
	topupID        = uuid.MustParse("019fee0c-1a2b-7c3d-8e4f-5a6b7c8d9e0f")
	transferID     = uuid.MustParse("019fee0c-1a2b-7c3d-8e4f-5a6b7c8d9e10")
	paymentID      = uuid.MustParse("019fee0c-1a2b-7c3d-8e4f-5a6b7c8d9e11")
	subscriptionID = uuid.MustParse("019fee0c-1a2b-7c3d-8e4f-5a6b7c8d9e12")
	eventID        = uuid.MustParse("019fedda-88ef-7253-8253-14d9e24723fb")
	endpointID     = uuid.MustParse("019fee0d-2c3d-7e4f-9a5b-6c7d8e9f0a1b")
	clientID       = uuid.MustParse("019fee0d-2c3d-7e4f-9a5b-6c7d8e9f0a1c")
	offeringID     = uuid.MustParse("019fee0d-2c3d-7e4f-9a5b-6c7d8e9f0a1d")
	orderID        = uuid.MustParse("019fee0d-2c3d-7e4f-9a5b-6c7d8e9f0a1e")
)

type capture struct {
	method string
	path   string
	query  url.Values
	idem   string
	body   string
}

// TestMethodRequestShapes pins every method in the v0.1 surface to the
// request it must send: verb, path, query, body, and — the one that costs
// real money to get wrong — whether an Idempotency-Key rides along.
func TestMethodRequestShapes(t *testing.T) {
	tests := []struct {
		name      string
		call      func(context.Context, *Client) error
		respond   string
		status    int
		wantPath  string
		wantVerb  string
		wantQuery url.Values
		wantIdem  string
		wantBody  map[string]any
	}{
		{
			name:     "Me",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Me(ctx); return err },
			respond:  `{"id":"` + userID.String() + `","external_id":"user-42"}`,
			wantVerb: http.MethodGet, wantPath: "/v1/me",
		},
		{
			name: "CreateUser",
			call: func(ctx context.Context, c *Client) error {
				name := "Ada Lovelace"
				_, err := c.CreateUser(ctx, CreateUserRequest{ExternalId: "user-42", DisplayName: &name})
				return err
			},
			status:   201,
			respond:  `{"id":"` + userID.String() + `","external_id":"user-42"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/users",
			wantBody: map[string]any{"external_id": "user-42", "display_name": "Ada Lovelace"},
		},
		{
			name:     "GetUser",
			call:     func(ctx context.Context, c *Client) error { _, err := c.GetUser(ctx, userID); return err },
			respond:  `{"id":"` + userID.String() + `"}`,
			wantVerb: http.MethodGet, wantPath: "/v1/users/" + userID.String(),
		},
		{
			name: "ListUsers",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListUsers(ctx, "ada", otherUserID, 25)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/users",
			wantQuery: url.Values{"query": {"ada"}, "cursor": {otherUserID.String()}, "limit": {"25"}},
		},
		{
			name:     "UserBalances",
			call:     func(ctx context.Context, c *Client) error { _, err := c.UserBalances(ctx, userID); return err },
			respond:  `[{"purpose":"cash","commodity":"USD","balance":5000,"available":5000,"held":0}]`,
			wantVerb: http.MethodGet, wantPath: "/v1/users/" + userID.String() + "/balances",
		},
		{
			name: "ListUserTransactions",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListUserTransactions(ctx, userID, uuid.Nil, 20)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/users/" + userID.String() + "/transactions",
			wantQuery: url.Values{"limit": {"20"}},
		},
		{
			name: "ListTransactions",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListTransactions(ctx, "transfer", uuid.Nil, 0)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/transactions",
			wantQuery: url.Values{"type": {"transfer"}},
		},
		{
			name: "SetUserCreditLimit",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.SetUserCreditLimit(ctx, userID, SetCreditLimitRequest{CreditLimit: 10000})
				return err
			},
			respond:  `{"credit_limit":10000}`,
			wantVerb: http.MethodPost, wantPath: "/v1/users/" + userID.String() + "/credit_limit",
			wantBody: map[string]any{"credit_limit": float64(10000)},
		},
		{
			name:     "ListTopupMethods",
			call:     func(ctx context.Context, c *Client) error { _, err := c.ListTopupMethods(ctx); return err },
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/topup_methods",
		},
		{
			name: "CreateTopup",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateTopup(ctx, "topup-user-42-first", CreateTopupRequest{
					UserId: userID, Amount: 5000, Gateway: "stripe",
				})
				return err
			},
			status:   201,
			respond:  `{"id":"` + topupID.String() + `","status":"created"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/topups",
			wantIdem: "topup-user-42-first",
			wantBody: map[string]any{"user_id": userID.String(), "amount": float64(5000), "gateway": "stripe"},
		},
		{
			name:     "GetTopup",
			call:     func(ctx context.Context, c *Client) error { _, err := c.GetTopup(ctx, topupID); return err },
			respond:  `{"id":"` + topupID.String() + `"}`,
			wantVerb: http.MethodGet, wantPath: "/v1/topups/" + topupID.String(),
		},
		{
			name:     "RefreshTopup",
			call:     func(ctx context.Context, c *Client) error { _, err := c.RefreshTopup(ctx, topupID); return err },
			respond:  `{"id":"` + topupID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/topups/" + topupID.String() + "/refresh",
		},
		{
			name: "CreateTransfer",
			call: func(ctx context.Context, c *Client) error {
				note := "lunch"
				_, err := c.CreateTransfer(ctx, "transfer-order-8891", CreateTransferRequest{
					FromUser: userID, ToUser: &otherUserID, Amount: 1500, Note: &note,
				})
				return err
			},
			status:   201,
			respond:  `{"id":"` + transferID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/transfers",
			wantIdem: "transfer-order-8891",
			wantBody: map[string]any{
				"from_user": userID.String(), "to_user": otherUserID.String(),
				"amount": float64(1500), "note": "lunch",
			},
		},
		{
			name: "CreatePayment",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreatePayment(ctx, "pay-1", CreatePaymentRequest{
					PayerUserId: userID, MerchantId: otherUserID, Amount: 2500, Mode: "authorize",
				})
				return err
			},
			status:   201,
			respond:  `{"id":"` + paymentID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/payments",
			wantIdem: "pay-1",
			wantBody: map[string]any{
				"payer_user_id": userID.String(), "merchant_id": otherUserID.String(),
				"amount": float64(2500), "mode": "authorize",
			},
		},
		{
			name:     "GetPayment",
			call:     func(ctx context.Context, c *Client) error { _, err := c.GetPayment(ctx, paymentID); return err },
			respond:  `{"id":"` + paymentID.String() + `"}`,
			wantVerb: http.MethodGet, wantPath: "/v1/payments/" + paymentID.String(),
		},
		{
			name: "CapturePayment partial",
			call: func(ctx context.Context, c *Client) error {
				amount := int64(1000)
				_, err := c.CapturePayment(ctx, paymentID, "cap-1", &amount)
				return err
			},
			respond:  `{"id":"` + paymentID.String() + `","captured_amount":1000}`,
			wantVerb: http.MethodPost, wantPath: "/v1/payments/" + paymentID.String() + "/capture",
			wantIdem: "cap-1",
			wantBody: map[string]any{"amount": float64(1000)},
		},
		{
			name: "CapturePayment full sends no amount",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CapturePayment(ctx, paymentID, "cap-2", nil)
				return err
			},
			respond:  `{"id":"` + paymentID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/payments/" + paymentID.String() + "/capture",
			wantIdem: "cap-2",
			wantBody: map[string]any{},
		},
		{
			name:     "VoidPayment",
			call:     func(ctx context.Context, c *Client) error { _, err := c.VoidPayment(ctx, paymentID); return err },
			respond:  `{"id":"` + paymentID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/payments/" + paymentID.String() + "/void",
		},
		{
			name: "CreateRefund",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateRefund(ctx, "refund-1", CreateRefundRequest{PaymentId: paymentID, Amount: 500})
				return err
			},
			status:   201,
			respond:  `{"id":"` + paymentID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/refunds",
			wantIdem: "refund-1",
			wantBody: map[string]any{"payment_id": paymentID.String(), "amount": float64(500)},
		},
		{
			name: "ConvertPoints",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ConvertPoints(ctx, "convert-1", ConvertPointsJSONRequestBody{UserId: userID, Points: 250})
				return err
			},
			status:   201,
			respond:  `{"points":250,"cash_credited":250}`,
			wantVerb: http.MethodPost, wantPath: "/v1/rewards/convert",
			wantIdem: "convert-1",
			wantBody: map[string]any{"user_id": userID.String(), "points": float64(250)},
		},
		{
			name: "ListUserRewards",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListUserRewards(ctx, userID, 10)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/users/" + userID.String() + "/rewards",
			wantQuery: url.Values{"limit": {"10"}},
		},
		{
			name: "CreateSubscription",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateSubscription(ctx, "sub-1", CreateSubscriptionRequest{
					UserId: userID, MerchantId: otherUserID, PlanName: "pro", Amount: 999, Interval: "monthly",
				})
				return err
			},
			status:   201,
			respond:  `{"id":"` + subscriptionID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/subscriptions",
			wantIdem: "sub-1",
			wantBody: map[string]any{
				"user_id": userID.String(), "merchant_id": otherUserID.String(),
				"plan_name": "pro", "amount": float64(999), "interval": "monthly",
			},
		},
		{
			name: "GetSubscription",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetSubscription(ctx, subscriptionID)
				return err
			},
			respond:  `{"id":"` + subscriptionID.String() + `"}`,
			wantVerb: http.MethodGet, wantPath: "/v1/subscriptions/" + subscriptionID.String(),
		},
		{
			name: "ListSubscriptions",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListSubscriptions(ctx, &userID, SubscriptionStatusActive, uuid.Nil, 50)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/subscriptions",
			wantQuery: url.Values{"user_id": {userID.String()}, "status": {"active"}, "limit": {"50"}},
		},
		{
			name: "PauseSubscription",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.PauseSubscription(ctx, subscriptionID)
				return err
			},
			respond:  `{"id":"` + subscriptionID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/subscriptions/" + subscriptionID.String() + "/pause",
		},
		{
			name: "ResumeSubscription",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ResumeSubscription(ctx, subscriptionID)
				return err
			},
			respond:  `{"id":"` + subscriptionID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/subscriptions/" + subscriptionID.String() + "/resume",
		},
		{
			name: "CancelSubscription",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CancelSubscription(ctx, subscriptionID)
				return err
			},
			respond:  `{"id":"` + subscriptionID.String() + `"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/subscriptions/" + subscriptionID.String() + "/cancel",
		},
		{
			name: "CreateWebhookEndpoint",
			call: func(ctx context.Context, c *Client) error {
				filters := []string{"topup.succeeded"}
				_, err := c.CreateWebhookEndpoint(ctx, CreateWebhookEndpointRequest{
					Url: "https://api.yourapp.com/webhooks/walletd", EventFilters: &filters,
				})
				return err
			},
			status:   201,
			respond:  `{"id":"` + endpointID.String() + `","secret":"whsec_9f2c4a"}`,
			wantVerb: http.MethodPost, wantPath: "/v1/webhook_endpoints",
			wantBody: map[string]any{
				"url":           "https://api.yourapp.com/webhooks/walletd",
				"event_filters": []any{"topup.succeeded"},
			},
		},
		{
			name:     "ListWebhookEndpoints",
			call:     func(ctx context.Context, c *Client) error { _, err := c.ListWebhookEndpoints(ctx); return err },
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/webhook_endpoints",
		},
		{
			name: "ListWebhookEvents",
			call: func(ctx context.Context, c *Client) error {
				since := time.Date(2026, 8, 11, 5, 12, 29, 0, time.UTC)
				_, err := c.ListWebhookEvents(ctx, "topup.succeeded", since, 50)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/webhook_events",
			wantQuery: url.Values{
				"type": {"topup.succeeded"}, "since": {"2026-08-11T05:12:29Z"}, "limit": {"50"},
			},
		},
		{
			name: "RedeliverWebhookEvent",
			call: func(ctx context.Context, c *Client) error {
				n, err := c.RedeliverWebhookEvent(ctx, eventID)
				if err == nil && n != 2 {
					t.Errorf("enqueued = %d, want 2", n)
				}
				return err
			},
			status:   202,
			respond:  `{"enqueued":2}`,
			wantVerb: http.MethodPost, wantPath: "/v1/webhook_events/" + eventID.String() + "/redeliver",
		},
		{
			name: "ListWebhookDeliveries",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListWebhookDeliveries(ctx, &endpointID, uuid.Nil, 100)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/webhook_deliveries",
			wantQuery: url.Values{"endpoint_id": {endpointID.String()}, "limit": {"100"}},
		},
		{
			name: "ListClients",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListClients(ctx, CatalogClientStatusActive, 100)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/clients",
			wantQuery: url.Values{"status": {"active"}, "limit": {"100"}},
		},
		{
			name:     "GetClient",
			call:     func(ctx context.Context, c *Client) error { _, err := c.GetClient(ctx, clientID); return err },
			respond:  `{"id":"` + clientID.String() + `"}`,
			wantVerb: http.MethodGet, wantPath: "/v1/clients/" + clientID.String(),
		},
		{
			name: "ListClientOfferings",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListClientOfferings(ctx, clientID, OfferingFilter{Status: "published", Query: "mug", LowStock: true}, uuid.Nil, 100)
				return err
			},
			respond:  `{"offerings":[]}`,
			wantVerb: http.MethodGet, wantPath: "/v1/clients/" + clientID.String() + "/offerings",
			wantQuery: url.Values{
				"status": {"published"}, "q": {"mug"}, "low_stock": {"true"}, "limit": {"100"},
			},
		},
		{
			name: "GetClientOffering",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetClientOffering(ctx, clientID, offeringID)
				return err
			},
			respond:  `{"id":"` + offeringID.String() + `"}`,
			wantVerb: http.MethodGet,
			wantPath: "/v1/clients/" + clientID.String() + "/offerings/" + offeringID.String(),
		},
		{
			name:     "ListOrders",
			call:     func(ctx context.Context, c *Client) error { _, err := c.ListOrders(ctx, uuid.Nil, 100); return err },
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/orders",
			wantQuery: url.Values{"limit": {"100"}},
		},
		{
			name:     "GetOrder",
			call:     func(ctx context.Context, c *Client) error { _, err := c.GetOrder(ctx, orderID); return err },
			respond:  `{"id":"` + orderID.String() + `"}`,
			wantVerb: http.MethodGet, wantPath: "/v1/orders/" + orderID.String(),
		},
		{
			name: "ListExploreClients",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ListExploreClients(ctx, "coffee", "food", uuid.Nil, 100)
				return err
			},
			respond:  `[]`,
			wantVerb: http.MethodGet, wantPath: "/v1/explore/clients",
			wantQuery: url.Values{"q": {"coffee"}, "category": {"food"}, "limit": {"100"}},
		},
		{
			name:     "ExploreClient",
			call:     func(ctx context.Context, c *Client) error { _, err := c.ExploreClient(ctx, clientID); return err },
			respond:  `{}`,
			wantVerb: http.MethodGet, wantPath: "/v1/explore/clients/" + clientID.String(),
		},
		{
			name:     "ExploreOffering",
			call:     func(ctx context.Context, c *Client) error { _, err := c.ExploreOffering(ctx, offeringID); return err },
			respond:  `{}`,
			wantVerb: http.MethodGet, wantPath: "/v1/explore/offerings/" + offeringID.String(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got capture
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				got = capture{
					method: r.Method,
					path:   r.URL.Path,
					query:  r.URL.Query(),
					idem:   r.Header.Get("Idempotency-Key"),
					body:   string(body),
				}
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, tc.respond)
			})

			if err := tc.call(t.Context(), c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got.method != tc.wantVerb {
				t.Errorf("method = %s, want %s", got.method, tc.wantVerb)
			}
			if got.path != tc.wantPath {
				t.Errorf("path = %s, want %s", got.path, tc.wantPath)
			}
			wantQuery := tc.wantQuery
			if wantQuery == nil {
				wantQuery = url.Values{}
			}
			if !reflect.DeepEqual(got.query, wantQuery) {
				t.Errorf("query = %v, want %v", got.query, wantQuery)
			}
			if got.idem != tc.wantIdem {
				t.Errorf("Idempotency-Key = %q, want %q", got.idem, tc.wantIdem)
			}
			if tc.wantBody == nil {
				if got.body != "" {
					t.Errorf("body = %q, want none", got.body)
				}
				return
			}
			var gotBody map[string]any
			if err := json.Unmarshal([]byte(got.body), &gotBody); err != nil {
				t.Fatalf("body %q is not JSON: %v", got.body, err)
			}
			if !reflect.DeepEqual(gotBody, tc.wantBody) {
				t.Errorf("body = %#v, want %#v", gotBody, tc.wantBody)
			}
		})
	}
}

// Every money-moving call in the contract must appear here with a key, and
// no read may accept one. The list is the contract's own: the twelve
// operations that declare the Idempotency-Key parameter, restricted to the
// ones this SDK exposes in v0.1.
func TestEveryKeyedOperationSendsTheHeader(t *testing.T) {
	keyed := []struct {
		name string
		call func(context.Context, *Client, IdempotencyKey) error
	}{
		{"CreateTopup", func(ctx context.Context, c *Client, k IdempotencyKey) error {
			_, err := c.CreateTopup(ctx, k, CreateTopupRequest{})
			return err
		}},
		{"CreateTransfer", func(ctx context.Context, c *Client, k IdempotencyKey) error {
			_, err := c.CreateTransfer(ctx, k, CreateTransferRequest{})
			return err
		}},
		{"CreatePayment", func(ctx context.Context, c *Client, k IdempotencyKey) error {
			_, err := c.CreatePayment(ctx, k, CreatePaymentRequest{})
			return err
		}},
		{"CapturePayment", func(ctx context.Context, c *Client, k IdempotencyKey) error {
			_, err := c.CapturePayment(ctx, paymentID, k, nil)
			return err
		}},
		{"CreateRefund", func(ctx context.Context, c *Client, k IdempotencyKey) error {
			_, err := c.CreateRefund(ctx, k, CreateRefundRequest{})
			return err
		}},
		{"ConvertPoints", func(ctx context.Context, c *Client, k IdempotencyKey) error {
			_, err := c.ConvertPoints(ctx, k, ConvertPointsJSONRequestBody{})
			return err
		}},
		{"CreateSubscription", func(ctx context.Context, c *Client, k IdempotencyKey) error {
			_, err := c.CreateSubscription(ctx, k, CreateSubscriptionRequest{})
			return err
		}},
	}

	for _, tc := range keyed {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				seen = r.Header.Get("Idempotency-Key")
				_, _ = io.WriteString(w, `{}`)
			})
			if err := tc.call(t.Context(), c, "the-key"); err != nil {
				t.Fatalf("call: %v", err)
			}
			if seen != "the-key" {
				t.Errorf("Idempotency-Key = %q, want %q", seen, "the-key")
			}
		})
	}
}

func TestBaseURLWithPathPrefix(t *testing.T) {
	var path string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, `{}`)
	})
	// Rebuild against a base URL that carries a prefix, the way a gateway
	// mounted under /api would be configured.
	prefixed, err := New(c.base.String()+"/api", APIKey("k"), fastRetries(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := prefixed.Me(t.Context()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if path != "/api/v1/me" {
		t.Errorf("path = %q, want /api/v1/me", path)
	}
}

func TestResponseDecoding(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[
			{"purpose":"cash","commodity":"USD","balance":5000,"available":4500,"held":500},
			{"purpose":"points","commodity":"POINTS","balance":120,"available":120,"held":0}
		]`)
	})
	balances, err := c.UserBalances(t.Context(), userID)
	if err != nil {
		t.Fatalf("UserBalances: %v", err)
	}
	if len(balances) != 2 {
		t.Fatalf("len = %d, want 2", len(balances))
	}
	if balances[0].Available != 4500 || balances[0].Held != 500 {
		t.Errorf("cash = %+v", balances[0])
	}
}

func TestMalformedResponseBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id": not json`)
	})
	if _, err := c.GetUser(t.Context(), userID); err == nil {
		t.Fatal("want a decode error")
	}
}
