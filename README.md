# walletd-go

The first-party Go SDK for the [WalletD](https://walletd.io) API.

Typed methods over the published contract, RFC 7807 problems decoded into
errors you can `errors.Is`, retries that are safe because money-moving calls
cannot be made without an idempotency key, cursor pagination that will not
drop a page, and webhook signature verification.

**Targets WalletD API `0.8.0`.** `types.gen.go` is generated from
`api/openapi.yaml`, a byte copy of the document published at
`https://docs.walletd.io/openapi.yaml`, so the types cannot drift from the
contract without a regenerate — and CI fails if they do.

Status: `v0.1.x`, pre-1.0. The surface below is stable in shape; the rest of
the 117-operation contract is reachable over plain HTTP until it lands here.

## Install

```bash
go get github.com/walletd-io/walletd-go
```

Requires Go 1.27 or newer.

## Quickstart

The five calls from the [quickstart](https://docs.walletd.io/quickstart/):
create a user, fund the wallet, read the balance, send money, read the
history.

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/walletd-io/walletd-go"
)

func main() {
	ctx := context.Background()

	client, err := walletd.New(os.Getenv("WALLETD_API"), walletd.APIKey(os.Getenv("WALLETD_API_KEY")),
		walletd.WithUserAgent("acme-billing/2.1"))
	if err != nil {
		log.Fatal(err)
	}

	// 1. Create a wallet user. external_id is yours and unique per tenant.
	name := "Ada Lovelace"
	handle := "ada"
	ada, err := client.CreateUser(ctx, walletd.CreateUserRequest{
		ExternalId:  "user-42",
		DisplayName: &name,
		Handle:      &handle,
	})
	if err != nil {
		log.Fatal(err)
	}

	// 2. Put money in. This does NOT credit the wallet: it opens an intent
	// at the gateway. The balance rises when the processor confirms, which
	// arrives as a topup.succeeded webhook.
	topup, err := client.CreateTopup(ctx, "topup-user-42-first", walletd.CreateTopupRequest{
		UserId:  ada.Id,
		Amount:  5000, // minor units: $50.00
		Gateway: "stripe",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("hand this to your checkout UI:", topup.NextAction)

	// 3. Read the balance. Show Available, not Balance: the difference is
	// money held by an uncaptured authorization.
	balances, err := client.UserBalances(ctx, ada.Id)
	if err != nil {
		log.Fatal(err)
	}
	for _, b := range balances {
		fmt.Printf("%s %s: %d available\n", b.Purpose, b.Commodity, b.Available)
	}

	// 4. Send money. The idempotency key is a parameter, not an option, so
	// a transfer without one does not compile. It comes from your order, so
	// retrying after a timeout still sends once.
	bob, err := client.CreateUser(ctx, walletd.CreateUserRequest{ExternalId: "user-43"})
	if err != nil {
		log.Fatal(err)
	}
	transfer, err := client.CreateTransfer(ctx, "transfer-order-8891", walletd.CreateTransferRequest{
		FromUser: ada.Id,
		ToUser:   &bob.Id,
		Amount:   1500,
	})
	switch {
	case errors.Is(err, walletd.ErrInsufficientFunds):
		fmt.Println("not enough money")
	case errors.Is(err, walletd.ErrTierLimitExceeded):
		fmt.Println("a tier cap would be breached; nothing reached the processor")
	case err != nil:
		log.Fatal(err)
	default:
		fmt.Println("sent", transfer.Id)
	}

	// 5. Read the history. The iterator pages for you.
	for row, err := range client.UserTransactions(ada.Id).All(ctx) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s %s %d %s\n", row.CreatedAt.Format("2006-01-02"), row.TxnType, row.Amount, row.Direction)
	}
}
```

## Credentials

```go
walletd.APIKey("sk_sandbox_...")  // a tenant key: acts for the whole loop
walletd.UserToken(accessToken)    // one wallet user: reaches only their money
walletd.CredentialFunc(fn)        // anything else: a token cache, a secret manager
```

`CredentialFunc` is called once per attempt, so a rotating credential
refreshes without rebuilding the client.

## Errors

HTTP 4xx/5xx failures return a `*walletd.Problem` using the contract's RFC 7807 schema.
Transport, decoding, cancellation and unexpected-status errors are separate.
**Branch on `Code`, never on `Title` or `Detail`:** the code is the stable
contract and the prose is not.

```go
_, err := client.CreatePayment(ctx, key, req)

switch {
case errors.Is(err, walletd.ErrInsufficientFunds):   // code insufficient_funds
case errors.Is(err, walletd.ErrTierLimitExceeded):   // code tier_limit_exceeded
case errors.Is(err, walletd.ErrInsufficientScope):   // code insufficient_scope
case errors.Is(err, walletd.ErrUnprocessable):       // any 422, code or not
case errors.Is(err, walletd.ErrTransport):           // missing or interrupted answer
case errors.Is(err, walletd.ErrUnexpectedStatus):    // unexpected non-2xx, including redirects
}

var p *walletd.Problem
if errors.As(err, &p) {
	log.Printf("walletd %d %s: %s", p.Status, p.Code, p.Message())
}
```

Each problem matches two sentinels: the one for its exact code, and the one
for its status class (`ErrBadRequest`, `ErrUnauthorized`, `ErrForbidden`,
`ErrNotFound`, `ErrConflict`, `ErrUnprocessable`, `ErrRateLimited`,
`ErrServer`). A code added to the API after this release still matches its
class, so a `switch` on classes never goes stale.

## Idempotency

Money-moving methods take an `IdempotencyKey` as an explicit parameter.
There is no option to omit it and no default: forgetting one is a compile
error rather than a duplicate payment found in production.

```go
func (c *Client) CreateTransfer(ctx context.Context, key walletd.IdempotencyKey, req CreateTransferRequest) (Transfer, error)
func (c *Client) CapturePayment(ctx context.Context, paymentID uuid.UUID, key walletd.IdempotencyKey, amount *int64) (Payment, error)
```

The key belongs to your logical operation, not to an HTTP attempt. Derive it
from something you already have and will still have after a restart — an
order number, an invoice line, a job id. `walletd.NewIdempotencyKey()` is
for when no such identifier exists, and it must be minted **once** and
stored with the work: a key regenerated per attempt charges the customer
twice.

Read methods do not accept a key, because the server would ignore one.

## Retries

The default policy is four attempts with equal-jitter exponential backoff
capped at five seconds.

| Answer | Retried |
|---|---|
| `429` | always — the request was refused before it was acted on, and `Retry-After` is honoured |
| `500`, `502`, `503`, `504` | only when replay is safe: a `GET`, or a call carrying an idempotency key |
| any other `4xx` | never — it is a statement about the request, and retrying only burns the rate budget |
| no answer at all | same rule as `5xx` |

A `Retry-After` longer than `MaxDelay` is not waited out: the problem comes
back so your scheduler decides, instead of the SDK parking a goroutine for
minutes. The request body is re-created from bytes on every attempt, never
replayed from a consumed reader.

```go
walletd.New(base, cred, walletd.WithRetryPolicy(walletd.RetryPolicy{
	MaxAttempts: 6,
	BaseDelay:   100 * time.Millisecond,
	MaxDelay:    10 * time.Second,
}))
walletd.New(base, cred, walletd.WithoutRetries())
```

Every call takes a `context.Context` and honours cancellation, including
while waiting between attempts.

## Pagination

```go
it := client.Users("ada")
for it.Next(ctx) {
	fmt.Println(it.Item().ExternalId)
}
if err := it.Err(); err != nil { ... }

// or, range-over-func
for u, err := range client.Users("").All(ctx) { ... }

// or, for small result sets
users, err := client.Users("").Collect(ctx)
```

The iterators exist because the obvious loop is wrong. These listings answer
with a bare array and no next-page marker, so the end has to be inferred —
and "fewer rows than I asked for means the end" is false for the user
history, which pages by *transaction* while returning one row per entry. The
iterators always send an explicit limit, count distinct cursor keys rather
than rows, and return `ErrPaginationStalled` rather than looping or stopping
quietly if the server ever fails to advance the cursor.

`PageSize(n)` is capped at 100, which is the ceiling the service enforces on
every listing, and a non-positive value selects the default. Asking for more
than the server will return is the one way to make a full page look like the
last one, so the cap is applied here rather than left to be discovered. Set it
before the first `Next`: later changes are ignored, including after a failed
fetch, so a page already buffered is never judged against a different limit.

Iterators: `Users`, `UserTransactions`, `Transactions`, `Subscriptions`,
`Orders`, `WebhookDeliveries`, `ExploreClients`, and `ClientOfferings`
(which follows the envelope's `next_cursor`).

## Response handling

Interrupted or empty expected JSON responses match `ErrTransport`. GET/HEAD and
keyed writes retry within the configured budget using the original request bytes
and key; unkeyed writes do not retry these failures. Valid but incompatible JSON
returns a decode error without retrying. Failed attempts never populate results.
Cancellation stops retries.

Only 2xx responses enter success decoding. Redirects are always refused, including
with `WithHTTPClient`: the SDK copies the supplied client and replaces its
`CheckRedirect` policy without mutating the original. Point the SDK directly at
the API host. Unexpected non-2xx statuses outside 4xx/5xx match
`ErrUnexpectedStatus`; a redirect cannot silently replay a write or turn it into
GET. These response and webhook validation changes are included in v0.1.2.

## Webhooks

Verifying the signature is not optional: your endpoint is a public URL.
`X-Wallet-Event-Id` is unsigned. Deduplicate using the verified body's
`Event.TenantID` and `Event.ID`; `ParseEvent` rejects missing or zero IDs.
The callback below must commit a durable inbox row with a unique tenant/event key;
a duplicate is successful acceptance. Only then acknowledge. Workers must commit
local effects and their processed marker atomically, or use an outbox and stable
idempotency key for remote effects. Check a configured tenant against the signed
envelope before acceptance.

```go
// enqueue commits a unique (TenantID, ID) inbox row before returning nil.
func handler(secret string, enqueue func(walletd.Event) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The RAW bytes are what was signed. Decoding and re-encoding the
		// JSON changes key order, and the hash with it.
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := walletd.VerifySignature(
			r.Header.Get(walletd.SignatureHeader), body, secret, walletd.DefaultTolerance,
		); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		event, err := walletd.ParseEvent(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

        // Only the signed body identifiers are authoritative.
        if err := enqueue(event); err != nil {
            w.WriteHeader(http.StatusServiceUnavailable)
            return
        }
		w.WriteHeader(http.StatusOK)
	}
}

```

`VerifySignature` implements `X-Wallet-Signature: t=<unix>,v1=<hex
hmac_sha256(secret, "<t>.<raw body>")>` over the raw bytes, compares in
constant time with `hmac.Equal`, and rejects a timestamp outside the
tolerance so a captured delivery cannot be replayed later. It returns
`ErrSignatureMalformed`, `ErrSignatureStale` or `ErrSignatureMismatch`; all
three mean reject the delivery.

One secret belongs to one endpoint of one tenant. If your receiver serves
several tenants, select the secret by the delivery's `tenant_id`.

## Surface in v0.1

| Area | Methods |
|---|---|
| Users | `Me`, `CreateUser`, `GetUser`, `ListUsers`, `Users` |
| Balances and history | `UserBalances`, `ListUserTransactions`, `UserTransactions`, `ListTransactions`, `Transactions` |
| Top-ups | `ListTopupMethods`, `CreateTopup`, `GetTopup`, `RefreshTopup` |
| Transfers | `CreateTransfer` |
| Payments | `CreatePayment`, `GetPayment`, `CapturePayment`, `VoidPayment`, `CreateRefund` |
| Subscriptions | `CreateSubscription`, `GetSubscription`, `ListSubscriptions`, `Subscriptions`, `PauseSubscription`, `ResumeSubscription`, `CancelSubscription` |
| Rewards | `ConvertPoints`, `ListUserRewards` |
| Credit | `SetUserCreditLimit` |
| Webhooks | `CreateWebhookEndpoint`, `ListWebhookEndpoints`, `ListWebhookEvents`, `RedeliverWebhookEvent`, `ListWebhookDeliveries`, `WebhookDeliveries` |
| Catalog and orders (read) | `ListClients`, `GetClient`, `ListClientOfferings`, `ClientOfferings`, `GetClientOffering`, `ListOrders`, `Orders`, `GetOrder`, `ListExploreClients`, `ExploreClients`, `ExploreClient`, `ExploreOffering` |

Anything not listed is reachable over plain HTTP with the same credential;
see the [API reference](https://docs.walletd.io/reference/).

## Regenerating the types

```bash
curl -sSfo api/openapi.yaml https://docs.walletd.io/openapi.yaml
make generate
```

`overlay.yaml` carries the one code-generation hint the SDK needs and leaves
`api/openapi.yaml` byte-identical to the published document. CI runs
`go generate` and fails on a diff.

## Development

```bash
make check   # gofmt, vet, build, test
make cover   # coverage
make smoke   # needs WALLETD_API and WALLETD_API_KEY against a sandbox
```

The smoke suite is behind the `smoke` build tag and talks to a live
deployment — the workspace's compose stack (`make up`) or a staging tenant.
`go test ./...` never reaches it.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Apache-2.0 rather than MIT for two reasons that matter to anyone building a
payment integration on it: it grants a patent licence explicitly, and it is
explicit that it grants no trademark rights. The permission you need to use
this client library is written down, and it is separate from any agreement
covering the WalletD platform itself.
