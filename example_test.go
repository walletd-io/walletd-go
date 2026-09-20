package walletd_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/walletd-io/walletd-go"
)

// Example is the quickstart in SDK form, and is the same code the README
// shows. It is compiled by `go test`, so the documented sample cannot rot
// past a contract change without the build saying so.
func Example() {
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

// ExampleVerifySignature is the README's receiver, compiled.
func ExampleVerifySignature() {
	secret := os.Getenv("WALLETD_WEBHOOK_SECRET")

	http.HandleFunc("/webhooks/walletd", func(w http.ResponseWriter, r *http.Request) {
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

		// Delivery is at-least-once: X-Wallet-Event-Id is the dedupe key.
		// Answer fast and do the work elsewhere.
		go func() {
			switch event.Type {
			case "topup.succeeded":
				var topup walletd.Topup
				if err := event.Into("topup", &topup); err != nil {
					return
				}
				fmt.Println("credited", topup.UserId, topup.Amount)
			default:
				// New types are added without warning. Ignore, do not crash.
			}
		}()
		w.WriteHeader(http.StatusOK)
	})
}

// ExampleClient_CreateTransfer shows branching on problem codes.
func ExampleClient_CreateTransfer() {
	client, err := walletd.New("https://apigw.walletd.io", walletd.APIKey("sk_sandbox_..."))
	if err != nil {
		log.Fatal(err)
	}

	// The key is the caller's order, minted once and reused on every
	// attempt — including the SDK's own retries.
	_, err = client.CreateTransfer(context.Background(), "transfer-order-8891",
		walletd.CreateTransferRequest{Amount: 1500})

	var problem *walletd.Problem
	switch {
	case errors.Is(err, walletd.ErrInsufficientFunds):
		fmt.Println("top up first")
	case errors.Is(err, walletd.ErrSelfTransfer):
		fmt.Println("sender and recipient are the same wallet")
	case errors.As(err, &problem):
		fmt.Printf("walletd %d %s: %s\n", problem.Status, problem.Code, problem.Message())
	}
}

// ExampleRetryPolicy tunes the retry budget for a latency-sensitive caller.
func ExampleRetryPolicy() {
	_, err := walletd.New("https://apigw.walletd.io", walletd.APIKey("sk_sandbox_..."),
		walletd.WithRetryPolicy(walletd.RetryPolicy{
			MaxAttempts: 6,
			BaseDelay:   100 * time.Millisecond,
			MaxDelay:    10 * time.Second,
		}))
	if err != nil {
		log.Fatal(err)
	}
}
