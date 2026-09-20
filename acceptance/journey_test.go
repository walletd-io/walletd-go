package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	walletd "github.com/walletd-io/walletd-go"
)

type evidence struct {
	Inbox, Effects, Pending int
	WorkerFailures          int `json:"worker_failures"`
	Attempts                []int
	Body, Signature         string
}

func waitFor(t *testing.T, condition func(evidence) bool) evidence {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get("http://127.0.0.1:28090/")
		if err == nil {
			var e evidence
			err = json.NewDecoder(r.Body).Decode(&e)
			_ = r.Body.Close()
			if err == nil && condition(e) {
				return e
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("receiver evidence condition timed out")
	return evidence{}
}

type cutTransport struct {
	mu       sync.Mutex
	requests [][]byte
	keys     []string
}

func (c *cutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	payment := r.Method == "POST" && r.URL.Path == "/v1/payments"
	first := false
	if payment {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		c.mu.Lock()
		c.requests = append(c.requests, b)
		c.keys = append(c.keys, r.Header.Get("Idempotency-Key"))
		first = len(c.requests) == 1
		c.mu.Unlock()
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && first && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader(`{"id":`))
		resp.ContentLength = -1
	}
	return resp, err
}
func TestInstalledSDKJourney(t *testing.T) {
	// Fixed loopback sandbox only; this test must never target a deployed tenant.
	raw, err := os.ReadFile(os.Getenv("WALLETD_ACCEPTANCE_CREDENTIALS"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct{ Tenant, Key, Base string }
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Base != "http://127.0.0.1:28080" || !strings.HasPrefix(cfg.Key, "sk_sandbox_") {
		t.Fatal("requires isolated local sandbox")
	}
	if walletd.Version != "0.1.2" {
		t.Fatal("wrong installed SDK version")
	}
	state := os.Getenv("WALLETD_ACCEPTANCE_STATE")
	if state == "" {
		t.Fatal("state directory required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cut := &cutTransport{}
	client, err := walletd.New(cfg.Base, walletd.APIKey(cfg.Key), walletd.WithHTTPClient(&http.Client{Transport: cut, Timeout: 15 * time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	kind := walletd.UserKind("business")
	user, err := client.CreateUser(ctx, walletd.CreateUserRequest{ExternalId: "acceptance-" + uuid.NewString(), Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SetUserCreditLimit(ctx, user.Id, walletd.SetCreditLimitRequest{CreditLimit: 10000})
	if err != nil {
		t.Fatal(err)
	}
	// Merchant creation is not wrapped by this SDK version; use its public HTTP contract.
	req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Base+"/v1/merchants", strings.NewReader(`{"name":"Acceptance merchant","external_id":"acceptance-`+uuid.NewString()+`"}`))
	req.Header.Set("Authorization", "Bearer "+cfg.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var merchant struct{ Id uuid.UUID }
	err = json.NewDecoder(resp.Body).Decode(&merchant)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 201 || merchant.Id == uuid.Nil {
		t.Fatalf("merchant creation status=%d err=%v", resp.StatusCode, err)
	}
	filter := []string{"payment.captured"}
	endpoint, err := client.CreateWebhookEndpoint(ctx, walletd.CreateWebhookEndpointRequest{Url: "http://walletd-acceptance-receiver:8089/hook", EventFilters: &filter})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Secret == nil {
		t.Fatal("missing receiver secret")
	}
	config, _ := json.Marshal(map[string]string{"secret": *endpoint.Secret, "tenant": cfg.Tenant})
	if err = os.WriteFile(filepath.Join(state, "config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reject", "worker-fail"} {
		if err = os.WriteFile(filepath.Join(state, name), []byte("inject"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	key := walletd.IdempotencyKey("acceptance-payment-" + uuid.NewString())
	input := walletd.CreatePaymentRequest{PayerUserId: user.Id, MerchantId: merchant.Id, Amount: 100, Mode: walletd.CreatePaymentRequestMode("instant")}
	first, err := client.CreatePayment(ctx, key, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.requests) != 2 || !bytes.Equal(cut.requests[0], cut.requests[1]) || cut.keys[0] != cut.keys[1] {
		t.Fatal("interrupted response did not replay identical bytes/key")
	}
	balances, err := client.UserBalances(ctx, user.Id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.CreatePayment(ctx, key, input)
	if err != nil || second.Id != first.Id {
		t.Fatalf("same-key replay failed: %v", err)
	}
	after, err := client.UserBalances(ctx, user.Id)
	if err != nil || !reflect.DeepEqual(balances, after) {
		t.Fatal("retry changed balances")
	}
	input.Amount = 101
	_, err = client.CreatePayment(ctx, key, input)
	if !errors.Is(err, walletd.ErrIdempotencyKeyReuse) {
		t.Fatalf("changed intent: %v", err)
	}
	waitFor(t, func(e evidence) bool {
		for _, status := range e.Attempts {
			if status == 503 {
				return e.Inbox == 0 && e.Effects == 0
			}
		}
		return false
	})
	events, err := client.ListWebhookEvents(ctx, "payment.captured", time.Time{}, 50)
	if err != nil || len(events) != 1 {
		t.Fatalf("event count=%d err=%v", len(events), err)
	}
	if err = os.Remove(filepath.Join(state, "reject")); err != nil {
		t.Fatal(err)
	}
	_, err = client.RedeliverWebhookEvent(ctx, events[0].Id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func(e evidence) bool { return e.Inbox == 1 && e.Pending == 1 && e.Effects == 0 && e.WorkerFailures > 0 })
	if err = exec.CommandContext(ctx, "docker", "restart", "walletd-acceptance-receiver").Run(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func(e evidence) bool { return e.Inbox == 1 && e.Pending == 1 && e.Effects == 0 })
	if err = os.Remove(filepath.Join(state, "worker-fail")); err != nil {
		t.Fatal(err)
	}
	e := waitFor(t, func(e evidence) bool { return e.Inbox == 1 && e.Pending == 0 && e.Effects == 1 })
	if err = walletd.VerifySignature(e.Signature, []byte(e.Body), *endpoint.Secret, walletd.DefaultTolerance); err != nil {
		t.Fatal(err)
	}
	event, err := walletd.ParseEvent([]byte(e.Body))
	if err != nil || event.ID != events[0].Id {
		t.Fatal("signed identity mismatch")
	}
	req, _ = http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:28090/hook", strings.NewReader(e.Body))
	req.Header.Set(walletd.SignatureHeader, e.Signature)
	req.Header.Set(walletd.EventIDHeader, uuid.NewString())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("duplicate refused")
	}
	e = waitFor(t, func(e evidence) bool { return e.Inbox == 1 && e.Effects == 1 && len(e.Attempts) >= 3 })
	t.Logf("SDK=%s payment=%s one event, same-key retry preserved balances; receiver inbox=%d effects=%d pending=%d; storage refusal and process restart recovered", walletd.Version, first.Id, e.Inbox, e.Effects, e.Pending)
}
