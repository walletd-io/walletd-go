package walletd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// seqUUID makes ids that sort the way the server's cursors do, so a test
// cursor is meaningful rather than decorative.
func seqUUID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("019fee0b-0000-7000-8000-%012d", n))
}

func TestIteratorWalksEveryPage(t *testing.T) {
	const total = 250
	var served int
	var cursors []string

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		limit := 100
		if l := r.URL.Query().Get("limit"); l != "" {
			_, _ = fmt.Sscanf(l, "%d", &limit)
		}
		page := make([]User, 0, limit)
		for range limit {
			if served >= total {
				break
			}
			served++
			page = append(page, User{Id: seqUUID(served), ExternalId: fmt.Sprintf("user-%d", served)})
		}
		_ = json.NewEncoder(w).Encode(page)
	})

	users, err := c.Users("").Collect(t.Context())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(users) != total {
		t.Fatalf("collected %d users, want %d", len(users), total)
	}
	// 100 + 100 + 50: the short third page ends it, and there is no
	// wasted fourth request.
	if len(cursors) != 3 {
		t.Fatalf("pages fetched = %d, want 3", len(cursors))
	}
	if cursors[0] != "" {
		t.Errorf("first page sent cursor %q, want none", cursors[0])
	}
	if cursors[1] != seqUUID(100).String() {
		t.Errorf("second page cursor = %q, want the 100th id", cursors[1])
	}
	for i, u := range users {
		if u.Id != seqUUID(i+1) {
			t.Fatalf("user %d = %s, out of order or dropped", i, u.Id)
		}
	}
}

// The user-history listing pages by TRANSACTION, and one transaction can
// carry several rows. A page of 100 transactions can be 150 rows, so
// "len(page) < limit means the end" would have stopped early here and
// dropped the rest of the history in silence.
func TestIteratorCountsDistinctKeysNotRows(t *testing.T) {
	const txnsPerPage = 3
	pages := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		var rows []UserTransaction
		if pages > 2 {
			_ = json.NewEncoder(w).Encode(rows)
			return
		}
		for i := range txnsPerPage {
			txn := seqUUID((pages-1)*txnsPerPage + i + 1)
			// A conversion: two rows, one transaction.
			rows = append(rows,
				UserTransaction{TxnId: txn, Purpose: "cash", Amount: -100},
				UserTransaction{TxnId: txn, Purpose: "points", Amount: 100},
			)
		}
		_ = json.NewEncoder(w).Encode(rows)
	})

	rows, err := c.UserTransactions(userID).PageSize(txnsPerPage).Collect(t.Context())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(rows) != 2*txnsPerPage*2 {
		t.Fatalf("collected %d rows from %d pages, want %d — a page was dropped",
			len(rows), pages, 2*txnsPerPage*2)
	}
}

func TestIteratorStopsOnAStalledCursor(t *testing.T) {
	// A server that keeps answering with the same full page would spin an
	// iterator forever. Saying so beats both looping and stopping quietly.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		page := []User{{Id: seqUUID(1)}, {Id: seqUUID(2)}}
		_ = json.NewEncoder(w).Encode(page)
	})

	it := c.Users("").PageSize(2)
	seen := 0
	for it.Next(t.Context()) {
		seen++
		if seen > 100 {
			t.Fatal("iterator looped forever")
		}
	}
	if !errors.Is(it.Err(), ErrPaginationStalled) {
		t.Fatalf("Err = %v, want ErrPaginationStalled", it.Err())
	}
}

func TestIteratorSurfacesErrors(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"title":"no","status":403,"code":"insufficient_scope"}`))
	})

	it := c.Users("")
	if it.Next(t.Context()) {
		t.Fatal("Next returned true on a failing listing")
	}
	if !errors.Is(it.Err(), ErrInsufficientScope) {
		t.Fatalf("Err = %v, want ErrInsufficientScope", it.Err())
	}
	// The error must not be swallowed by a second Next.
	if it.Next(t.Context()) {
		t.Fatal("Next returned true after an error")
	}
}

func TestIteratorAllYieldsTheError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"title":"down","status":503,"code":"internal_error"}`))
	})

	var got error
	for _, err := range c.Orders().All(t.Context()) {
		if err != nil {
			got = err
		}
	}
	if !errors.Is(got, ErrServer) {
		t.Fatalf("yielded error = %v, want ErrServer", got)
	}
}

func TestIteratorAllStopsOnBreak(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := make([]Order, 100)
		for i := range page {
			page[i] = Order{Id: seqUUID(calls*1000 + i)}
		}
		_ = json.NewEncoder(w).Encode(page)
	})

	seen := 0
	for range c.Orders().All(t.Context()) {
		seen++
		if seen == 3 {
			break
		}
	}
	if seen != 3 {
		t.Fatalf("seen = %d, want 3", seen)
	}
	if calls != 1 {
		t.Errorf("fetched %d pages after a break, want 1", calls)
	}
}

func TestIteratorEmptyListing(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`[]`))
	})
	users, err := c.Users("").Collect(t.Context())
	if err != nil || len(users) != 0 {
		t.Fatalf("Collect = %v, %v", users, err)
	}
	if calls != 1 {
		t.Errorf("fetched %d pages for an empty listing, want 1", calls)
	}
}

func TestIteratorAlwaysSendsALimit(t *testing.T) {
	// The end-of-listing test compares the page against the limit we
	// asked for. Letting the server pick its own default would make that
	// comparison meaningless.
	var limits []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		limits = append(limits, r.URL.Query().Get("limit"))
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.Users("").Collect(t.Context()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(limits) != 1 || limits[0] != "100" {
		t.Fatalf("limits sent = %v, want one explicit 100", limits)
	}
}

func TestPageIteratorFollowsNextCursor(t *testing.T) {
	pages := 0
	var cursors []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		pages++
		body := OfferingPage{Offerings: []Offering{{Id: seqUUID(pages)}}}
		if pages < 3 {
			next := seqUUID(pages)
			body.NextCursor = &next
		}
		_ = json.NewEncoder(w).Encode(body)
	})

	offerings, err := c.ClientOfferings(clientID, OfferingFilter{}).Collect(t.Context())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(offerings) != 3 {
		t.Fatalf("collected %d offerings, want 3", len(offerings))
	}
	if len(cursors) != 3 || cursors[0] != "" || cursors[1] != seqUUID(1).String() {
		t.Fatalf("cursors = %v", cursors)
	}
}

// A short page that still names a next cursor is not the end: the envelope
// says so, and guessing from the length would drop the remainder.
func TestPageIteratorTrustsTheEnvelopeNotTheLength(t *testing.T) {
	pages := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		body := OfferingPage{Offerings: []Offering{{Id: seqUUID(pages)}}}
		if pages == 1 {
			next := seqUUID(1)
			body.NextCursor = &next
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	offerings, err := c.ClientOfferings(clientID, OfferingFilter{}).PageSize(100).Collect(t.Context())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(offerings) != 2 {
		t.Fatalf("collected %d, want 2 — the second page was dropped", len(offerings))
	}
}

func TestPageIteratorStallGuard(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		next := seqUUID(1)
		_ = json.NewEncoder(w).Encode(OfferingPage{
			Offerings:  []Offering{{Id: seqUUID(1)}},
			NextCursor: &next,
		})
	})
	it := c.ClientOfferings(clientID, OfferingFilter{})
	seen := 0
	for it.Next(t.Context()) {
		seen++
		if seen > 100 {
			t.Fatal("iterator looped forever")
		}
	}
	if !errors.Is(it.Err(), ErrPaginationStalled) {
		t.Fatalf("Err = %v, want ErrPaginationStalled", it.Err())
	}
}

func TestPageIteratorSurfacesErrors(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"title":"gone","status":404,"code":"client_not_found"}`))
	})
	_, err := c.ClientOfferings(clientID, OfferingFilter{}).Collect(t.Context())
	if !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("err = %v, want ErrClientNotFound", err)
	}
}

func TestIteratorPageSizeIsHonoured(t *testing.T) {
	var limit string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		limit = r.URL.Query().Get("limit")
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.Transactions("payment").PageSize(7).Collect(t.Context()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if limit != "7" {
		t.Errorf("limit = %q, want 7", limit)
	}
}

func TestIteratorRespectsContextCancellation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		page := make([]User, 100)
		for i := range page {
			page[i] = User{Id: seqUUID(i + 1)}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	ctx, cancel := context.WithCancel(t.Context())
	it := c.Users("")
	if !it.Next(ctx) {
		t.Fatalf("first Next: %v", it.Err())
	}
	cancel()
	for it.Next(ctx) {
	}
	if !errors.Is(it.Err(), context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", it.Err())
	}
	if !strings.Contains(it.Err().Error(), "context canceled") {
		t.Errorf("Err message = %q", it.Err())
	}
}

func TestOtherIteratorsPage(t *testing.T) {
	tests := []struct {
		name string
		walk func(*Client) func(context.Context) ([]uuid.UUID, error)
		path string
	}{
		{
			name: "Subscriptions",
			path: "/v1/subscriptions",
			walk: func(c *Client) func(context.Context) ([]uuid.UUID, error) {
				return func(ctx context.Context) ([]uuid.UUID, error) {
					var ids []uuid.UUID
					for s, err := range c.Subscriptions(&userID, SubscriptionStatusActive).PageSize(2).All(ctx) {
						if err != nil {
							return nil, err
						}
						ids = append(ids, s.Id)
					}
					return ids, nil
				}
			},
		},
		{
			name: "ExploreClients",
			path: "/v1/explore/clients",
			walk: func(c *Client) func(context.Context) ([]uuid.UUID, error) {
				return func(ctx context.Context) ([]uuid.UUID, error) {
					var ids []uuid.UUID
					for cl, err := range c.ExploreClients("coffee", "").PageSize(2).All(ctx) {
						if err != nil {
							return nil, err
						}
						ids = append(ids, cl.Id)
					}
					return ids, nil
				}
			},
		},
		{
			name: "WebhookDeliveries",
			path: "/v1/webhook_deliveries",
			walk: func(c *Client) func(context.Context) ([]uuid.UUID, error) {
				return func(ctx context.Context) ([]uuid.UUID, error) {
					var ids []uuid.UUID
					for d, err := range c.WebhookDeliveries(nil).PageSize(2).All(ctx) {
						if err != nil {
							return nil, err
						}
						ids = append(ids, d.Id)
					}
					return ids, nil
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			served := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %s, want %s", r.URL.Path, tc.path)
				}
				var page []map[string]any
				for range 2 {
					if served >= 3 {
						break
					}
					served++
					page = append(page, map[string]any{"id": seqUUID(served).String()})
				}
				_ = json.NewEncoder(w).Encode(page)
			})
			ids, err := tc.walk(c)(t.Context())
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if len(ids) != 3 {
				t.Fatalf("collected %d, want 3", len(ids))
			}
		})
	}
}

func TestPageIteratorAll(t *testing.T) {
	pages := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		body := OfferingPage{Offerings: []Offering{{Id: seqUUID(pages)}}}
		if pages == 1 {
			next := seqUUID(1)
			body.NextCursor = &next
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	seen := 0
	for _, err := range c.ClientOfferings(clientID, OfferingFilter{}).All(t.Context()) {
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("seen = %d, want 2", seen)
	}
}

func TestPageIteratorAllYieldsTheError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"title":"boom","status":500,"code":"internal_error"}`))
	})
	var got error
	for _, err := range c.ClientOfferings(clientID, OfferingFilter{}).All(t.Context()) {
		if err != nil {
			got = err
		}
	}
	if !errors.Is(got, ErrInternal) {
		t.Fatalf("yielded = %v, want ErrInternal", got)
	}
}

func TestPageIteratorBreakStopsFetching(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		next := seqUUID(calls)
		_ = json.NewEncoder(w).Encode(OfferingPage{
			Offerings:  []Offering{{Id: seqUUID(calls)}, {Id: seqUUID(calls + 100)}},
			NextCursor: &next,
		})
	})
	for range c.ClientOfferings(clientID, OfferingFilter{}).All(t.Context()) {
		break
	}
	if calls != 1 {
		t.Errorf("fetched %d pages after a break, want 1", calls)
	}
}
