package walletd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetries keeps the retry tests honest about ordering and counts
// without spending real time asleep.
func fastRetries(attempts int) Option {
	return WithRetryPolicy(RetryPolicy{MaxAttempts: attempts, BaseDelay: time.Millisecond, MaxDelay: 20 * time.Millisecond})
}

func testClient(t *testing.T, h http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, APIKey("sk_test_key"), append([]Option{fastRetries(1)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewValidatesArguments(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		cred    Credential
		wantErr bool
	}{
		{name: "ok", baseURL: "https://apigw.walletd.io", cred: APIKey("k")},
		{name: "ok with trailing slash", baseURL: "https://apigw.walletd.io/", cred: APIKey("k")},
		{name: "nil credential", baseURL: "https://apigw.walletd.io", wantErr: true},
		{name: "no scheme", baseURL: "apigw.walletd.io", cred: APIKey("k"), wantErr: true},
		{name: "empty", baseURL: "", cred: APIKey("k"), wantErr: true},
		{name: "unparseable", baseURL: "https://exa mple.com/%zz", cred: APIKey("k"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.baseURL, tc.cred)
			if (err != nil) != tc.wantErr {
				t.Fatalf("New(%q) error = %v, wantErr %v", tc.baseURL, err, tc.wantErr)
			}
		})
	}
}

func TestRequestCarriesCredentialAndUserAgent(t *testing.T) {
	var got *http.Request
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = io.WriteString(w, `{}`)
	}, WithUserAgent("acme-billing/2.1"))

	if _, err := c.Me(t.Context()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer sk_test_key" {
		t.Errorf("Authorization = %q", h)
	}
	if ua := got.Header.Get("User-Agent"); ua != "acme-billing/2.1 walletd-go/"+Version {
		t.Errorf("User-Agent = %q", ua)
	}
	if a := got.Header.Get("Accept"); !strings.Contains(a, "application/problem+json") {
		t.Errorf("Accept = %q, want problem+json", a)
	}
	if got.Header.Get("Idempotency-Key") != "" {
		t.Error("a read must not send an idempotency key")
	}
}

func TestUserTokenCredential(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL, UserToken("eyJhbGc"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Me(t.Context()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if auth != "Bearer eyJhbGc" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestCredentialErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	c, err := New(srv.URL, CredentialFunc(func(context.Context) (string, error) {
		return "", errors.New("vault down")
	}), fastRetries(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Me(t.Context()); err == nil || !strings.Contains(err.Error(), "vault down") {
		t.Fatalf("err = %v, want the credential error", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("server saw %d requests, want 0", n)
	}
}

func TestProblemDecoding(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		contentType  string
		body         string
		wantCode     string
		wantStatus   int
		wantSentinel error
		wantClass    error
	}{
		{
			name: "insufficient funds", status: 422, contentType: "application/problem+json",
			body:         `{"type":"about:blank","title":"Insufficient funds","status":422,"detail":"available 300, needed 1500","code":"insufficient_funds"}`,
			wantCode:     CodeInsufficientFunds,
			wantStatus:   422,
			wantSentinel: ErrInsufficientFunds,
			wantClass:    ErrUnprocessable,
		},
		{
			name: "tier limit", status: 422, contentType: "application/problem+json",
			body:         `{"title":"Tier limit exceeded","status":422,"code":"tier_limit_exceeded"}`,
			wantCode:     CodeTierLimitExceeded,
			wantStatus:   422,
			wantSentinel: ErrTierLimitExceeded,
			wantClass:    ErrUnprocessable,
		},
		{
			name: "idempotency reuse", status: 409, contentType: "application/problem+json",
			body:         `{"title":"Key reused","status":409,"code":"idempotency_key_reuse"}`,
			wantCode:     CodeIdempotencyKeyReuse,
			wantStatus:   409,
			wantSentinel: ErrIdempotencyKeyReuse,
			wantClass:    ErrConflict,
		},
		{
			name: "insufficient scope", status: 403, contentType: "application/problem+json",
			body:         `{"title":"Insufficient scope","status":403,"code":"insufficient_scope"}`,
			wantCode:     CodeInsufficientScope,
			wantStatus:   403,
			wantSentinel: ErrInsufficientScope,
			wantClass:    ErrForbidden,
		},
		{
			name: "unknown code still matches its class", status: 404, contentType: "application/problem+json",
			body:       `{"title":"Nope","status":404,"code":"widget_not_found"}`,
			wantCode:   "widget_not_found",
			wantStatus: 404,
			wantClass:  ErrNotFound,
		},
		{
			name: "status on the wire wins over the envelope", status: 429, contentType: "application/problem+json",
			body:       `{"title":"Slow down","status":200,"code":"rate_limited"}`,
			wantCode:   CodeRateLimited,
			wantStatus: 429,
			wantClass:  ErrRateLimited,
		},
		{
			name: "proxy html error is still a Problem", status: 502, contentType: "text/html",
			body:       `<html><body>502 Bad Gateway</body></html>`,
			wantCode:   "",
			wantStatus: 502,
			wantClass:  ErrServer,
		},
		{
			name: "empty body", status: 503, contentType: "application/problem+json",
			body:       ``,
			wantCode:   "",
			wantStatus: 503,
			wantClass:  ErrServer,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})

			_, err := c.GetUser(t.Context(), userID)
			if err == nil {
				t.Fatal("want an error")
			}
			p, ok := AsProblem(err)
			if !ok {
				t.Fatalf("err = %v, want a *Problem", err)
			}
			if p.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", p.Code, tc.wantCode)
			}
			if p.Status != tc.wantStatus {
				t.Errorf("Status = %d, want %d", p.Status, tc.wantStatus)
			}
			if p.Title == "" {
				t.Error("Title is empty; a caller logging it would see nothing")
			}
			if CodeOf(err) != tc.wantCode {
				t.Errorf("CodeOf = %q, want %q", CodeOf(err), tc.wantCode)
			}
			if tc.wantSentinel != nil && !errors.Is(err, tc.wantSentinel) {
				t.Errorf("errors.Is(%v) = false, want true", tc.wantSentinel)
			}
			if tc.wantClass != nil && !errors.Is(err, tc.wantClass) {
				t.Errorf("errors.Is(class %v) = false, want true", tc.wantClass)
			}
			if errors.Is(err, ErrInsufficientFunds) && tc.wantSentinel != ErrInsufficientFunds {
				t.Error("matched an unrelated sentinel")
			}
		})
	}
}

func TestProblemMessagePrefersDetail(t *testing.T) {
	detail := "available 300, needed 1500"
	p := &Problem{Title: "Insufficient funds", Status: 422, Code: CodeInsufficientFunds, Detail: &detail}
	if got := p.Message(); got != detail {
		t.Errorf("Message() = %q, want the detail", got)
	}
	if !strings.Contains(p.Error(), CodeInsufficientFunds) {
		t.Errorf("Error() = %q, want the code in it", p.Error())
	}
	bare := &Problem{Title: "Nope", Status: 404, Code: "x"}
	if got := bare.Message(); got != "Nope" {
		t.Errorf("Message() = %q, want the title", got)
	}
}

func TestAsProblemOnNonHTTPError(t *testing.T) {
	if _, ok := AsProblem(errors.New("plain")); ok {
		t.Error("AsProblem matched a non-HTTP error")
	}
	if CodeOf(nil) != "" {
		t.Error("CodeOf(nil) should be empty")
	}
}

func TestRetryPolicy(t *testing.T) {
	tests := []struct {
		name        string
		statuses    []int // one per attempt; the last repeats
		retryAfter  string
		call        func(context.Context, *Client) error
		maxAttempts int
		wantCalls   int32
		wantErr     bool
	}{
		{
			name: "429 retries a read", statuses: []int{429, 200},
			call:        func(ctx context.Context, c *Client) error { _, err := c.GetUser(ctx, userID); return err },
			maxAttempts: 4, wantCalls: 2,
		},
		{
			name: "429 retries a keyed write", statuses: []int{429, 201},
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateTransfer(ctx, "k-1", CreateTransferRequest{Amount: 100})
				return err
			},
			maxAttempts: 4, wantCalls: 2,
		},
		{
			name: "429 retries an unkeyed write, because it was refused not acted on", statuses: []int{429, 200},
			call:        func(ctx context.Context, c *Client) error { _, err := c.VoidPayment(ctx, paymentID); return err },
			maxAttempts: 4, wantCalls: 2,
		},
		{
			name: "503 retries a keyed write", statuses: []int{503, 503, 201},
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateTopup(ctx, "k-2", CreateTopupRequest{Amount: 5000})
				return err
			},
			maxAttempts: 4, wantCalls: 3,
		},
		{
			name: "500 does NOT retry an unkeyed write", statuses: []int{500},
			call:        func(ctx context.Context, c *Client) error { _, err := c.VoidPayment(ctx, paymentID); return err },
			maxAttempts: 4, wantCalls: 1, wantErr: true,
		},
		{
			name: "422 is never retried", statuses: []int{422},
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateTransfer(ctx, "k-3", CreateTransferRequest{Amount: 100})
				return err
			},
			maxAttempts: 4, wantCalls: 1, wantErr: true,
		},
		{
			name: "409 is never retried", statuses: []int{409},
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateTransfer(ctx, "k-4", CreateTransferRequest{Amount: 100})
				return err
			},
			maxAttempts: 4, wantCalls: 1, wantErr: true,
		},
		{
			name: "401 is never retried", statuses: []int{401},
			call:        func(ctx context.Context, c *Client) error { _, err := c.GetUser(ctx, userID); return err },
			maxAttempts: 4, wantCalls: 1, wantErr: true,
		},
		{
			name: "attempts are bounded", statuses: []int{503},
			call:        func(ctx context.Context, c *Client) error { _, err := c.GetUser(ctx, userID); return err },
			maxAttempts: 3, wantCalls: 3, wantErr: true,
		},
		{
			name: "retries can be switched off", statuses: []int{503},
			call:        func(ctx context.Context, c *Client) error { _, err := c.GetUser(ctx, userID); return err },
			maxAttempts: 1, wantCalls: 1, wantErr: true,
		},
		{
			name: "Retry-After in seconds is honoured", statuses: []int{429, 200}, retryAfter: "0",
			call:        func(ctx context.Context, c *Client) error { _, err := c.GetUser(ctx, userID); return err },
			maxAttempts: 4, wantCalls: 2,
		},
		{
			name: "a Retry-After past the cap stops the retry", statuses: []int{429, 200}, retryAfter: "600",
			call:        func(ctx context.Context, c *Client) error { _, err := c.GetUser(ctx, userID); return err },
			maxAttempts: 4, wantCalls: 1, wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				status := tc.statuses[min(n, len(tc.statuses))-1]
				if status >= 400 && tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				if status >= 400 {
					_, _ = io.WriteString(w, `{"title":"nope","status":`+strconv.Itoa(status)+`,"code":"internal_error"}`)
				} else {
					_, _ = io.WriteString(w, `{}`)
				}
			}, fastRetries(tc.maxAttempts))

			err := tc.call(t.Context(), c)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("server saw %d requests, want %d", got, tc.wantCalls)
			}
		})
	}
}

// A retry is only safe because the key is stable. If the SDK ever minted a
// key per attempt this test fails, and the failure mode it stands in for is
// a customer charged twice.
func TestRetryKeepsTheSameIdempotencyKeyAndBody(t *testing.T) {
	var keys []string
	var bodies []string
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		bodies = append(bodies, string(body))
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"`+transferID.String()+`","amount":1500}`)
	}, fastRetries(4))

	tr, err := c.CreateTransfer(t.Context(), "transfer-order-8891", CreateTransferRequest{
		Amount: 1500, FromUser: userID, ToUser: &otherUserID,
	})
	if err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	if tr.Amount != 1500 {
		t.Errorf("Amount = %d", tr.Amount)
	}
	if len(keys) != 3 {
		t.Fatalf("attempts = %d, want 3", len(keys))
	}
	for i, k := range keys {
		if k != "transfer-order-8891" {
			t.Errorf("attempt %d key = %q, want the caller's key unchanged", i+1, k)
		}
	}
	// The body must be rewound, not consumed: a second attempt sending an
	// empty body is the classic io.Reader retry bug.
	for i, b := range bodies {
		if b != bodies[0] || b == "" {
			t.Errorf("attempt %d body = %q, want %q", i+1, b, bodies[0])
		}
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}, WithRetryPolicy(RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 10 * time.Second}))

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.GetUser(ctx, userID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waited %s past the deadline", elapsed)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

func TestContextCancellationBeforeTheCall(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request should never have been sent")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetUser(ctx, userID); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestTransportFailure(t *testing.T) {
	c, err := New("http://127.0.0.1:1", APIKey("k"), fastRetries(2))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetUser(t.Context(), userID); !errors.Is(err, ErrTransport) {
		t.Fatalf("err = %v, want ErrTransport", err)
	}
}

func TestIdempotencyKeyValidation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an invalid key must be refused before the request is sent")
	})
	tests := []struct {
		name string
		key  IdempotencyKey
	}{
		{"empty", ""},
		{"too long", IdempotencyKey(strings.Repeat("k", 201))},
		{"newline", "key\nInjected: header"},
		{"non ascii", "ключ"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.CreateTransfer(t.Context(), tc.key, CreateTransferRequest{Amount: 1})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestNewIdempotencyKeyIsUnique(t *testing.T) {
	a, b := NewIdempotencyKey(), NewIdempotencyKey()
	if a == b || a == "" {
		t.Fatalf("keys %q and %q", a, b)
	}
	if err := a.validate(); err != nil {
		t.Fatalf("generated key is invalid: %v", err)
	}
	if a.String() != string(a) {
		t.Error("String() must round-trip")
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		in     string
		wantOK bool
		want   time.Duration
	}{
		{"", false, 0},
		{"5", true, 5 * time.Second},
		{" 5 ", true, 5 * time.Second},
		{"-1", false, 0},
		{"soon", false, 0},
	}
	for _, tc := range tests {
		got, ok := parseRetryAfter(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("parseRetryAfter(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
	// An HTTP-date in the past is a zero wait, not a negative one.
	if got, ok := parseRetryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)); !ok || got != 0 {
		t.Errorf("past HTTP-date = %v, %v; want 0, true", got, ok)
	}
	if got, ok := parseRetryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)); !ok || got <= 0 {
		t.Errorf("future HTTP-date = %v, %v", got, ok)
	}
}

func TestBackoffStaysUnderTheCap(t *testing.T) {
	c, err := New("https://example.test", APIKey("k"),
		WithRetryPolicy(RetryPolicy{MaxAttempts: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for attempt := 1; attempt <= 9; attempt++ {
		for range 50 {
			d := c.backoff(attempt)
			if d < 0 || d > time.Second {
				t.Fatalf("backoff(%d) = %v, outside [0, 1s]", attempt, d)
			}
		}
	}
}

func TestWithHTTPClient(t *testing.T) {
	var used bool
	custom := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"id":"` + userID.String() + `"}`)),
			Header:     http.Header{},
			Request:    r,
		}, nil
	})}
	c, err := New("https://example.test", APIKey("k"), WithHTTPClient(custom), WithHTTPClient(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Me(t.Context()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if !used {
		t.Error("the custom http.Client was not used")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWithoutRetriesAndPolicyFloor(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}, WithoutRetries())
	if _, err := c.GetUser(t.Context(), userID); err == nil {
		t.Fatal("want an error")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}

	// A policy asking for zero attempts would send nothing at all; it is
	// clamped to one rather than silently doing nothing.
	floored, err := New("https://example.test", APIKey("k"), WithRetryPolicy(RetryPolicy{MaxAttempts: 0}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if floored.retry.MaxAttempts != 1 {
		t.Errorf("MaxAttempts = %d, want 1", floored.retry.MaxAttempts)
	}
}

func TestDefaultUserAgentAndEmptyOverride(t *testing.T) {
	var ua string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{}`)
	}, WithUserAgent(""))
	if _, err := c.Me(t.Context()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if ua != "walletd-go/"+Version {
		t.Errorf("User-Agent = %q, want the default", ua)
	}
}

func TestEmptyCredentialsAreRefused(t *testing.T) {
	for _, cred := range []Credential{APIKey(""), UserToken("")} {
		c, err := New("https://example.test", cred)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := c.Me(t.Context()); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("err = %v, want ErrInvalidRequest", err)
		}
	}
}
