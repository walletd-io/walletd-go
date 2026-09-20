package walletd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func responseClient(t *testing.T, f responseTransport) *Client {
	t.Helper()
	c, err := New("https://api.example", APIKey("test"), WithHTTPClient(&http.Client{Transport: f}), fastRetries(3))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestInterruptedSuccessRetry(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		key          IdempotencyKey
		attempts     int
	}{
		{"read", "GET", "", 2}, {"keyed write", "POST", "saved-key", 2}, {"unkeyed write", "POST", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := responseClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				data, _ := io.ReadAll(r.Body)
				if string(data) != `{"amount":100}` || r.Header.Get("Idempotency-Key") != string(tc.key) {
					t.Fatalf("request changed: %s %v", data, r.Header)
				}
				body := `{"stale":1,"fresh":`
				if calls > 1 {
					body = `{"fresh":2}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			out := map[string]int{"original": 3}
			err := c.do(t.Context(), tc.method, "/test", tc.key, nil, map[string]int{"amount": 100}, &out)
			if calls != tc.attempts {
				t.Fatalf("calls=%d want=%d", calls, tc.attempts)
			}
			if tc.attempts == 1 {
				if !errors.Is(err, ErrTransport) || len(out) != 1 || out["original"] != 3 {
					t.Fatalf("error=%v out=%v", err, out)
				}
			} else if err != nil || len(out) != 1 || out["fresh"] != 2 {
				t.Fatalf("error=%v out=%v", err, out)
			}
		})
	}
}

type failedBody struct {
	err    error
	cancel context.CancelFunc
}

func (b failedBody) Read([]byte) (int, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return 0, b.err
}
func (failedBody) Close() error { return nil }
func TestResponseReadFailuresAndDecodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		readErr    error
		cancel     bool
		attempts   int
		want       error
	}{
		{name: "empty", attempts: 3, want: ErrTransport},
		{name: "truncated", body: `{"a":`, attempts: 3, want: ErrTransport},
		{name: "read reset", readErr: io.ErrClosedPipe, attempts: 3, want: ErrTransport},
		{name: "cancel", readErr: io.ErrClosedPipe, cancel: true, attempts: 1, want: context.Canceled},
		{name: "schema", body: `{"a":"wrong"}`, attempts: 1},
		{name: "syntax", body: `{"a":!}`, attempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			c := responseClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				body := io.NopCloser(strings.NewReader(tc.body))
				if tc.readErr != nil {
					b := failedBody{err: tc.readErr}
					if tc.cancel {
						b.cancel = cancel
					}
					body = b
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			})
			out := map[string]int{"original": 1}
			err := c.do(ctx, "GET", "/test", "", nil, nil, &out)
			if err == nil || calls != tc.attempts || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.want == nil && errors.Is(err, ErrTransport)) {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if len(out) != 1 || out["original"] != 1 {
				t.Fatalf("partial result escaped: %v", out)
			}
		})
	}
}
func TestRedirectsAreRefused(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, location := range []string{"", "/destination"} {
			t.Run(http.StatusText(status)+location, func(t *testing.T) {
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Path != "/test" {
						t.Error("redirect followed")
					}
					if location != "" {
						w.Header().Set("Location", location)
					}
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{}`)
				}))
				defer srv.Close()
				for _, custom := range []bool{false, true} {
					opts := []Option{fastRetries(3)}
					redirectCalls := 0
					h := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { redirectCalls++; return http.ErrUseLastResponse }}
					if custom {
						opts = append(opts, WithHTTPClient(h))
					}
					c, err := New(srv.URL, APIKey("test"), opts...)
					if err != nil {
						t.Fatal(err)
					}
					var out map[string]any
					err = c.do(t.Context(), "POST", "/test", "", nil, map[string]int{"n": 1}, &out)
					if !errors.Is(err, ErrUnexpectedStatus) || out != nil || redirectCalls != 0 {
						t.Fatalf("err=%v out=%v redirects=%d", err, out, redirectCalls)
					}
					if custom && h.CheckRedirect == nil {
						t.Fatal("caller client mutated")
					}
				}
				if calls != 2 {
					t.Fatalf("calls=%d", calls)
				}
			})
		}
	}
}
func TestEmptySuccessAndInformationalStatus(t *testing.T) {
	for _, status := range []int{101, 204, 302, 600} {
		c := responseClient(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		err := c.do(t.Context(), "DELETE", "/test", "", nil, nil, nil)
		if status == 204 && err != nil || status != 204 && !errors.Is(err, ErrUnexpectedStatus) {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
}
