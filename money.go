package walletd

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"
)

// Money-moving calls take an IdempotencyKey as an explicit parameter, so
// that omitting one is a compile error rather than a duplicate payment
// found in production. Pass the SAME key to every attempt of one logical
// operation, including your own retries after a timeout — see the
// IdempotencyKey documentation.

// ListTopupMethods returns the funding methods this tenant has enabled.
func (c *Client) ListTopupMethods(ctx context.Context) ([]TopupMethod, error) {
	var out []TopupMethod
	err := c.do(ctx, http.MethodGet, "/v1/topup_methods", "", nil, nil, &out)
	return out, err
}

// CreateTopup opens a funding intent at a gateway. It does NOT credit the
// wallet: the balance rises only when the processor confirms, which arrives
// as a topup.succeeded webhook. Hand NextAction to your checkout UI.
func (c *Client) CreateTopup(ctx context.Context, key IdempotencyKey, req CreateTopupRequest) (Topup, error) {
	var out Topup
	err := c.doKeyed(ctx, http.MethodPost, "/v1/topups", key, req, &out)
	return out, err
}

// GetTopup reads a top-up as WalletD currently sees it.
func (c *Client) GetTopup(ctx context.Context, topupID uuid.UUID) (Topup, error) {
	var out Topup
	err := c.do(ctx, http.MethodGet, "/v1/topups/"+topupID.String(), "", nil, nil, &out)
	return out, err
}

// RefreshTopup asks WalletD to reconcile the intent against the gateway
// now, instead of waiting for the processor's callback. Use it when a
// top-up has been pending longer than it should; it is the reconcile path,
// not a polling loop.
func (c *Client) RefreshTopup(ctx context.Context, topupID uuid.UUID) (Topup, error) {
	var out Topup
	err := c.do(ctx, http.MethodPost, "/v1/topups/"+topupID.String()+"/refresh", "", nil, nil, &out)
	return out, err
}

// CreateTransfer moves money between two wallets of the same tenant.
func (c *Client) CreateTransfer(ctx context.Context, key IdempotencyKey, req CreateTransferRequest) (Transfer, error) {
	var out Transfer
	err := c.doKeyed(ctx, http.MethodPost, "/v1/transfers", key, req, &out)
	return out, err
}

// CreatePayment charges a user for a merchant. Mode "capture" takes the
// money now; mode "authorize" holds it until CapturePayment or VoidPayment,
// or until the hold expires on its own.
func (c *Client) CreatePayment(ctx context.Context, key IdempotencyKey, req CreatePaymentRequest) (Payment, error) {
	var out Payment
	err := c.doKeyed(ctx, http.MethodPost, "/v1/payments", key, req, &out)
	return out, err
}

// GetPayment reads one payment.
func (c *Client) GetPayment(ctx context.Context, paymentID uuid.UUID) (Payment, error) {
	var out Payment
	err := c.do(ctx, http.MethodGet, "/v1/payments/"+paymentID.String(), "", nil, nil, &out)
	return out, err
}

// CapturePayment settles an authorized payment. A nil amount captures the
// full authorization; a smaller one captures part and releases the rest.
func (c *Client) CapturePayment(ctx context.Context, paymentID uuid.UUID, key IdempotencyKey, amount *int64) (Payment, error) {
	var out Payment
	err := c.doKeyed(ctx, http.MethodPost, "/v1/payments/"+paymentID.String()+"/capture", key,
		CapturePaymentJSONRequestBody{Amount: amount}, &out)
	return out, err
}

// VoidPayment releases an authorization without charging.
//
// The contract does not key this call: voiding twice is the same void, so
// there is nothing to deduplicate. That also means the SDK will not retry
// it after a 5xx — read the payment back and decide.
func (c *Client) VoidPayment(ctx context.Context, paymentID uuid.UUID) (Payment, error) {
	var out Payment
	err := c.do(ctx, http.MethodPost, "/v1/payments/"+paymentID.String()+"/void", "", nil, nil, &out)
	return out, err
}

// CreateRefund returns part or all of a captured payment. Refunds are
// contra postings, not reversals: the original payment stays in the books.
func (c *Client) CreateRefund(ctx context.Context, key IdempotencyKey, req CreateRefundRequest) (Refund, error) {
	var out Refund
	err := c.doKeyed(ctx, http.MethodPost, "/v1/refunds", key, req, &out)
	return out, err
}

// ConvertPoints turns a user's loyalty points into spendable cash at the
// tenant's active conversion rule.
func (c *Client) ConvertPoints(ctx context.Context, key IdempotencyKey, req ConvertPointsJSONRequestBody) (Conversion, error) {
	var out Conversion
	err := c.doKeyed(ctx, http.MethodPost, "/v1/rewards/convert", key, req, &out)
	return out, err
}

// ListUserRewards returns a user's most recent reward grants.
//
// The contract gives this listing a limit but no cursor, so it cannot be
// paged: what you get is the newest limit grants, at most 100.
func (c *Client) ListUserRewards(ctx context.Context, userID uuid.UUID, limit int) ([]RewardGrant, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []RewardGrant
	err := c.do(ctx, http.MethodGet, "/v1/users/"+userID.String()+"/rewards", "", q, nil, &out)
	return out, err
}

// CreateSubscription starts a recurring charge. The first charge happens at
// StartAt, or immediately when it is nil.
func (c *Client) CreateSubscription(ctx context.Context, key IdempotencyKey, req CreateSubscriptionRequest) (Subscription, error) {
	var out Subscription
	err := c.doKeyed(ctx, http.MethodPost, "/v1/subscriptions", key, req, &out)
	return out, err
}

// GetSubscription reads one subscription.
func (c *Client) GetSubscription(ctx context.Context, subscriptionID uuid.UUID) (Subscription, error) {
	var out Subscription
	err := c.do(ctx, http.MethodGet, "/v1/subscriptions/"+subscriptionID.String(), "", nil, nil, &out)
	return out, err
}

// ListSubscriptions reads one page. Prefer Subscriptions.
func (c *Client) ListSubscriptions(ctx context.Context, userID *uuid.UUID, status SubscriptionStatus, cursor uuid.UUID, limit int) ([]Subscription, error) {
	q := pageQuery(cursor, limit)
	if userID != nil {
		q.Set("user_id", userID.String())
	}
	if status != "" {
		q.Set("status", string(status))
	}
	var out []Subscription
	err := c.do(ctx, http.MethodGet, "/v1/subscriptions", "", q, nil, &out)
	return out, err
}

// Subscriptions iterates subscriptions, optionally filtered by user and
// status.
func (c *Client) Subscriptions(userID *uuid.UUID, status SubscriptionStatus) *Iterator[Subscription] {
	return newIterator(
		func(ctx context.Context, cursor uuid.UUID, limit int) ([]Subscription, error) {
			return c.ListSubscriptions(ctx, userID, status, cursor, limit)
		},
		func(s Subscription) uuid.UUID { return s.Id },
	)
}

// PauseSubscription stops charging without ending the mandate.
func (c *Client) PauseSubscription(ctx context.Context, subscriptionID uuid.UUID) (Subscription, error) {
	return c.subscriptionAction(ctx, subscriptionID, "pause")
}

// ResumeSubscription restarts a paused subscription.
func (c *Client) ResumeSubscription(ctx context.Context, subscriptionID uuid.UUID) (Subscription, error) {
	return c.subscriptionAction(ctx, subscriptionID, "resume")
}

// CancelSubscription ends a subscription for good.
func (c *Client) CancelSubscription(ctx context.Context, subscriptionID uuid.UUID) (Subscription, error) {
	return c.subscriptionAction(ctx, subscriptionID, "cancel")
}

// The three lifecycle actions are unkeyed in the contract: each is a state
// transition that repeats to the same state, so there is nothing for an
// idempotency key to deduplicate.
func (c *Client) subscriptionAction(ctx context.Context, subscriptionID uuid.UUID, action string) (Subscription, error) {
	var out Subscription
	err := c.do(ctx, http.MethodPost, "/v1/subscriptions/"+subscriptionID.String()+"/"+action, "", nil, nil, &out)
	return out, err
}
