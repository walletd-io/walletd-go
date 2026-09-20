package walletd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const (
	// Version is this SDK's version.
	Version = "0.1.0"
	// APIVersion is the version of the WalletD contract types.gen.go was
	// generated from. A server on a different minor version may return
	// fields this SDK does not know; it will never return a field shape
	// this SDK decodes wrongly, because unknown fields are ignored.
	APIVersion = "0.8.0"
)

// Credential authorizes every request. Implementations must be safe for
// concurrent use; Token is called once per attempt, so a rotating
// credential refreshes without the client being rebuilt.
type Credential interface {
	Token(ctx context.Context) (string, error)
}

type credentialFunc func(context.Context) (string, error)

func (f credentialFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// APIKey is a tenant API key (`sk_{env}_{id}_{secret}`). It carries the
// tenant's own scopes and acts for the whole loop: never ship one to an
// end-user device.
func APIKey(key string) Credential {
	return credentialFunc(func(context.Context) (string, error) {
		if key == "" {
			return "", fmt.Errorf("%w: empty API key", ErrInvalidRequest)
		}
		return key, nil
	})
}

// UserToken is a wallet user's own access token. It resolves to that user
// and can only reach that user's money.
func UserToken(token string) Credential {
	return credentialFunc(func(context.Context) (string, error) {
		if token == "" {
			return "", fmt.Errorf("%w: empty user token", ErrInvalidRequest)
		}
		return token, nil
	})
}

// CredentialFunc adapts any token source — an OAuth2 client-credentials
// cache, a secret manager lookup — to Credential.
func CredentialFunc(f func(ctx context.Context) (string, error)) Credential {
	return credentialFunc(f)
}

// RetryPolicy bounds automatic retries.
//
// Replay-safe transport failures (including interrupted response bodies) are
// retried. HTTP retries cover 429 (refused before it was acted on), plus
// 500/502/503/504 when the request is replay-safe: GET/HEAD or a call
// carrying an idempotency key. Every other 4xx is a statement about the
// request itself and is returned unchanged; retrying it would only waste
// the rate budget.
type RetryPolicy struct {
	// MaxAttempts counts the first attempt. 1 disables retries.
	MaxAttempts int
	// BaseDelay is the first backoff step; each further step doubles it.
	BaseDelay time.Duration
	// MaxDelay caps one wait. A Retry-After longer than this is not
	// waited out: the problem is returned so the caller decides, rather
	// than the SDK blocking a request goroutine for minutes.
	MaxDelay time.Duration
}

// DefaultRetryPolicy is four attempts over roughly two seconds of backoff.
var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts: 4,
	BaseDelay:   200 * time.Millisecond,
	MaxDelay:    5 * time.Second,
}

// Client talks to one WalletD deployment as one principal. It is safe for
// concurrent use and should be built once and shared.
type Client struct {
	base      *url.URL
	http      *http.Client
	cred      Credential
	userAgent string
	retry     RetryPolicy
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client, for a custom transport, proxy,
// timeout or instrumentation. Redirects are always refused by the SDK; the
// supplied client is copied and its CheckRedirect policy is not used.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithRetryPolicy replaces the retry policy.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(c *Client) {
		if p.MaxAttempts < 1 {
			p.MaxAttempts = 1
		}
		c.retry = p
	}
}

// WithoutRetries sends every request exactly once.
func WithoutRetries() Option {
	return func(c *Client) { c.retry.MaxAttempts = 1 }
}

// WithUserAgent replaces the User-Agent. Identify your application; the
// SDK name and version are appended so support can see both.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua + " walletd-go/" + Version
		}
	}
}

// New builds a client for baseURL — the API host, with no path, e.g.
// https://apigw.walletd.io.
func New(baseURL string, cred Credential, opts ...Option) (*Client, error) {
	if cred == nil {
		return nil, fmt.Errorf("%w: nil credential", ErrInvalidRequest)
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("walletd base url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%w: base url %q needs a scheme and host", ErrInvalidRequest, baseURL)
	}

	// One client talks to one host at whatever concurrency its callers
	// run, and net/http's DefaultTransport keeps only 2 idle connections
	// per host: under load that opens a fresh connection for nearly every
	// call until the ephemeral port range is spent in TIME_WAIT. Sized
	// from the same measurement that fixed it in the ledgerd SDK.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 256
	transport.MaxConnsPerHost = 256

	c := &Client{
		base:      u,
		http:      &http.Client{Timeout: 30 * time.Second, Transport: transport},
		cred:      cred,
		userAgent: "walletd-go/" + Version,
		retry:     DefaultRetryPolicy,
	}
	for _, opt := range opts {
		opt(c)
	}
	// Never redirect credentialed API operations or silently rewrite POST to GET.
	h := *c.http
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &h
	return c, nil
}

// doKeyed is do for a money-moving call: the key is mandatory and is
// checked before anything is sent, so an empty or unusable one is a local
// error rather than a request the server has to refuse.
func (c *Client) doKeyed(ctx context.Context, method, path string, key IdempotencyKey, in, out any) error {
	if err := key.validate(); err != nil {
		return err
	}
	return c.do(ctx, method, path, key, nil, in, out)
}

// do sends one request, retrying within the policy.
//
// idem is empty for reads and for the calls the contract does not key. On a
// money-moving call it is the caller's key, passed down from a typed
// parameter — and it is marshalled ONCE, before the retry loop, so every
// attempt of one logical operation carries the SAME key. That is the whole
// reason retrying a POST is safe here: the second attempt either performs
// the operation (the first never landed) or replays the stored answer of
// the first. Generating a key per attempt would turn one retry into two
// payments, so no code below this line may mint one.
func (c *Client) do(ctx context.Context, method, path string, idem IdempotencyKey, query url.Values, in, out any) error {
	var payload []byte
	if in != nil {
		var err error
		payload, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%w: encode request: %w", ErrInvalidRequest, err)
		}
	}
	if idem != "" {
		if err := idem.validate(); err != nil {
			return err
		}
	}

	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	target := u.String()

	// A 5xx may be retried only when replaying the request cannot double
	// an effect: a read, or a write the server deduplicates by key.
	replaySafe := method == http.MethodGet || method == http.MethodHead || idem != ""

	for attempt := 1; ; attempt++ {
		// A fresh reader per attempt: the body is rewound, never a
		// consumed stream. Nothing here reads from an io.Reader the
		// caller owns, so a retry can never send a truncated body.
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return fmt.Errorf("walletd request: %w", err)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
			req.ContentLength = int64(len(payload))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(payload)), nil
			}
		}
		req.Header.Set("Accept", "application/json, application/problem+json")
		req.Header.Set("User-Agent", c.userAgent)
		if idem != "" {
			req.Header.Set("Idempotency-Key", string(idem))
		}
		token, err := c.cred.Token(ctx)
		if err != nil {
			return fmt.Errorf("walletd credential: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.http.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("walletd %s %s: %w", method, path, ctxErr)
			}
			if replaySafe && attempt < c.retry.MaxAttempts {
				if werr := c.wait(ctx, c.backoff(attempt)); werr != nil {
					return werr
				}
				continue
			}
			return fmt.Errorf("%w: %s %s: %w", ErrTransport, method, path, err)
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 600 {
			problem := decodeProblem(resp)
			retryAfter, hasRetryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			drainClose(resp)
			if !c.retryable(resp.StatusCode, replaySafe, attempt) {
				return problem
			}
			delay := c.backoff(attempt)
			if hasRetryAfter {
				// The server named a time. Honour it, but do not
				// block for minutes on the SDK's own account: past
				// the cap, hand the problem back and let the caller
				// schedule the work.
				if retryAfter > c.retry.MaxDelay {
					return problem
				}
				delay = retryAfter
			}
			if err := c.wait(ctx, delay); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			drainClose(resp)
			return fmt.Errorf("%w: %s %s: HTTP %d", ErrUnexpectedStatus, method, path, resp.StatusCode)
		}

		if out == nil {
			drainClose(resp)
			return nil
		}
		// Decode each attempt into fresh state; never expose a partial answer.
		result := reflect.New(reflect.TypeOf(out).Elem())
		data, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var decErr error
		if readErr == nil {
			decErr = json.NewDecoder(bytes.NewReader(data)).Decode(result.Interface())
		}
		if readErr != nil || errors.Is(decErr, io.EOF) || errors.Is(decErr, io.ErrUnexpectedEOF) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if replaySafe && attempt < c.retry.MaxAttempts {
				if err := c.wait(ctx, c.backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			if readErr == nil {
				readErr = decErr
			}
			return fmt.Errorf("%w: read %s %s: %w", ErrTransport, method, path, readErr)
		}
		if decErr != nil {
			return fmt.Errorf("walletd decode %s %s: %w", method, path, decErr)
		}
		reflect.ValueOf(out).Elem().Set(result.Elem())
		return nil
	}
}

func (c *Client) retryable(status int, replaySafe bool, attempt int) bool {
	if attempt >= c.retry.MaxAttempts {
		return false
	}
	switch status {
	case http.StatusTooManyRequests:
		// Refused before it was acted on, whatever the method.
		return true
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return replaySafe
	default:
		// Every other 4xx is about the request. Retrying cannot change
		// the answer and only burns the rate budget.
		return false
	}
}

// backoff is exponential with full jitter, capped. Jitter matters because
// a fleet retrying a shared outage in lockstep is the outage's second wave.
func (c *Client) backoff(attempt int) time.Duration {
	base := c.retry.BaseDelay
	if base <= 0 {
		return 0
	}
	d := base << (attempt - 1)
	if d > c.retry.MaxDelay || d <= 0 {
		d = c.retry.MaxDelay
	}
	if d <= 0 {
		return 0
	}
	// Equal jitter: wait at least half the step, at most the whole one.
	half := int64(d) / 2
	return time.Duration(half + rand.Int64N(half+1))
}

func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("walletd retry: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

// parseRetryAfter reads both forms RFC 9110 allows: delay-seconds and an
// HTTP-date.
func parseRetryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(v); err == nil {
		d := time.Until(when)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// drainClose returns the connection to the pool: a close with bytes left
// unread kills it, which under load looks like random dial failures.
func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}
