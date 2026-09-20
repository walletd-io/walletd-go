package walletd

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// Model the service's 100-item ceiling and verify completeness through the
// actual iterators. In particular, a request for 101 must not silently make
// the first full server page look terminal.
func TestPaginationPageSizeBounds(t *testing.T) {
	for _, requested := range []int{-1, 0, 1, 100, 101} {
		for _, envelope := range []bool{false, true} {
			t.Run(fmt.Sprintf("size=%d/envelope=%v", requested, envelope), func(t *testing.T) {
				wantLimit := requested
				if wantLimit < 1 || wantLimit > 100 {
					wantLimit = 100
				}
				served := 0
				fetch := func(_ context.Context, _ uuid.UUID, limit int) ([]User, error) {
					if limit != wantLimit {
						t.Fatalf("sent limit %d, want %d", limit, wantLimit)
					}
					page := []User{}
					for i := 0; i < min(limit, 100) && served < 150; i++ {
						served++
						page = append(page, User{Id: seqUUID(served)})
					}
					return page, nil
				}
				var got []User
				var err error
				if envelope {
					it := newPageIterator(func(ctx context.Context, c uuid.UUID, n int) ([]User, *uuid.UUID, error) {
						page, e := fetch(ctx, c, n)
						if served >= 150 {
							return page, nil, e
						}
						next := page[len(page)-1].Id
						return page, &next, e
					}).PageSize(requested)
					if !it.Next(t.Context()) {
						t.Fatal("missing first row", it.Err())
					}
					got = append(got, it.Item())
					it.PageSize(2) // Late changes must not alter buffered or future pages.
					var rest []User
					rest, err = it.Collect(t.Context())
					got = append(got, rest...)
				} else {
					it := newIterator(fetch, func(u User) uuid.UUID { return u.Id }).PageSize(requested)
					if !it.Next(t.Context()) {
						t.Fatal("missing first row", it.Err())
					}
					got = append(got, it.Item())
					it.PageSize(2)
					var rest []User
					rest, err = it.Collect(t.Context())
					got = append(got, rest...)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != 150 {
					t.Fatalf("got %d users, want 150", len(got))
				}
				for i, u := range got {
					if u.Id != seqUUID(i+1) {
						t.Fatalf("missing or duplicate row at %d", i)
					}
				}
			})
		}
	}
}
