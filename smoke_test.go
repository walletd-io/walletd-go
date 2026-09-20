//go:build smoke

// Package walletd's smoke suite runs the quickstart's five calls against a
// live deployment — the compose stack from the workspace root (`make up`),
// a staging tenant, or anything else that speaks the contract.
//
// It is behind the `smoke` build tag, so `go test ./...` never reaches it
// and CI stays hermetic. Run it deliberately:
//
//	export WALLETD_API=http://localhost:8080
//	export WALLETD_API_KEY=sk_sandbox_...
//	go test -tags smoke -run TestSmoke -v ./...
//
// It moves real money in whatever environment it is pointed at. Point it at
// a sandbox tenant.
package walletd

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func smokeClient(t *testing.T) *Client {
	t.Helper()
	if testing.Short() {
		t.Skip("smoke test needs a live deployment")
	}
	base, key := os.Getenv("WALLETD_API"), os.Getenv("WALLETD_API_KEY")
	if base == "" || key == "" {
		t.Skip("set WALLETD_API and WALLETD_API_KEY to run the smoke suite")
	}
	c, err := New(base, APIKey(key), WithUserAgent("walletd-go-smoke/"+Version))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestSmokeQuickstart is the quickstart, in SDK form: create a user, fund
// it, read the balance, send money, read the history. It is the same
// sequence the documentation shows, so a change that breaks the docs breaks
// this first.
func TestSmokeQuickstart(t *testing.T) {
	c := smokeClient(t)
	ctx := t.Context()
	run := uuid.NewString()[:8]

	// 1. Create two wallet users.
	name := "Ada Lovelace"
	sender, err := c.CreateUser(ctx, CreateUserRequest{
		ExternalId: "smoke-sender-" + run, DisplayName: &name,
	})
	if err != nil {
		t.Fatalf("CreateUser sender: %v", err)
	}
	recipient, err := c.CreateUser(ctx, CreateUserRequest{ExternalId: "smoke-recipient-" + run})
	if err != nil {
		t.Fatalf("CreateUser recipient: %v", err)
	}

	// 2. Put money in. The wallet is NOT credited here: the intent has to
	// clear at the gateway first, which is why the balance below is
	// allowed to still be zero.
	topup, err := c.CreateTopup(ctx, IdempotencyKey("smoke-topup-"+run), CreateTopupRequest{
		UserId: sender.Id, Amount: 5000, Gateway: "mock",
	})
	if err != nil && !errors.Is(err, ErrBadRequest) {
		t.Fatalf("CreateTopup: %v", err)
	}
	if err == nil {
		if _, err := c.GetTopup(ctx, topup.Id); err != nil {
			t.Fatalf("GetTopup: %v", err)
		}
		if _, err := c.RefreshTopup(ctx, topup.Id); err != nil {
			t.Logf("RefreshTopup: %v (the gateway may not support reconcile here)", err)
		}
	}

	// 3. Read the balance.
	balances, err := c.UserBalances(ctx, sender.Id)
	if err != nil {
		t.Fatalf("UserBalances: %v", err)
	}
	t.Logf("sender balances: %+v", balances)

	// 4. Send money. A wallet with no cash refuses with insufficient_funds,
	// which is a correct answer from a correctly wired deployment.
	note := "smoke"
	_, err = c.CreateTransfer(ctx, IdempotencyKey("smoke-transfer-"+run), CreateTransferRequest{
		FromUser: sender.Id, ToUser: &recipient.Id, Amount: 100, Note: &note,
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrInsufficientFunds), errors.Is(err, ErrTierLimitExceeded):
		t.Logf("transfer refused as expected on an unfunded wallet: %v", err)
	default:
		t.Fatalf("CreateTransfer: %v", err)
	}

	// 5. Read the history, through the iterator.
	rows, err := c.UserTransactions(sender.Id).PageSize(10).Collect(ctx)
	if err != nil {
		t.Fatalf("UserTransactions: %v", err)
	}
	t.Logf("sender history: %d rows", len(rows))
}

// TestSmokeIdempotencyReplays proves the property the whole retry design
// rests on: the same key returns the same object, and never a second one.
func TestSmokeIdempotencyReplays(t *testing.T) {
	c := smokeClient(t)
	ctx := t.Context()
	run := uuid.NewString()[:8]

	user, err := c.CreateUser(ctx, CreateUserRequest{ExternalId: "smoke-idem-" + run})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	key := IdempotencyKey("smoke-idem-topup-" + run)
	req := CreateTopupRequest{UserId: user.Id, Amount: 1000, Gateway: "mock"}

	first, err := c.CreateTopup(ctx, key, req)
	if err != nil {
		t.Skipf("no usable gateway here: %v", err)
	}
	second, err := c.CreateTopup(ctx, key, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.Id != second.Id {
		t.Fatalf("the same key produced two top-ups: %s and %s", first.Id, second.Id)
	}

	// A different body under the same key is a conflict, not a replay.
	req.Amount = 2000
	if _, err := c.CreateTopup(ctx, key, req); !errors.Is(err, ErrIdempotencyKeyReuse) {
		t.Fatalf("reuse with different parameters = %v, want ErrIdempotencyKeyReuse", err)
	}
}

// TestSmokeWebhookRoundTrip registers an endpoint, signs a body with the
// secret the platform just issued, and verifies it with this SDK. It is the
// end-to-end check that the scheme in the docs is the scheme in the code.
func TestSmokeWebhookRoundTrip(t *testing.T) {
	c := smokeClient(t)
	ctx := t.Context()

	filters := []string{"topup.succeeded"}
	endpoint, err := c.CreateWebhookEndpoint(ctx, CreateWebhookEndpointRequest{
		Url:          "https://example.invalid/walletd-smoke/" + uuid.NewString(),
		EventFilters: &filters,
	})
	if err != nil {
		t.Fatalf("CreateWebhookEndpoint: %v", err)
	}
	if endpoint.Secret == nil || *endpoint.Secret == "" {
		t.Fatal("no secret returned; it is issued exactly once, here")
	}

	body := []byte(`{"id":"` + uuid.NewString() + `","type":"topup.succeeded","tenant_id":"` + uuid.NewString() + `","created_at":"2026-08-11T05:12:29.922Z","data":{}}`)
	header := sign(*endpoint.Secret, time.Now().Unix(), body)
	if err := VerifySignature(header, body, *endpoint.Secret, DefaultTolerance); err != nil {
		t.Fatalf("VerifySignature against a live secret: %v", err)
	}
	if _, err := c.ListWebhookEndpoints(ctx); err != nil {
		t.Fatalf("ListWebhookEndpoints: %v", err)
	}
}
