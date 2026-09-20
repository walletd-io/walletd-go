package walletd

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/google/uuid"
)

// DefaultPageSize is the page the iterators ask for. The contract's ceiling
// is 100.
const DefaultPageSize = 100

// ErrPaginationStalled means the server answered a full page whose last
// item is the cursor we just sent. Continuing would loop forever and
// stopping would drop whatever lies past it, so the iterator says so
// instead of doing either quietly.
var ErrPaginationStalled = errors.New("pagination stalled: the page did not advance the cursor")

// Iterator walks a cursor-paged listing one item at a time, fetching the
// next page only when the current one runs out.
//
// It exists because the obvious loop is wrong. These listings answer with a
// bare array and no next-page marker, so the end of the list has to be
// inferred — and "fewer rows than I asked for means the end" is false for
// at least one of them: a user's transaction history pages by TRANSACTION,
// and one transaction can carry several rows for the same user (a
// conversion debits cash and credits points). A full page there can be more
// rows than the limit, and a short page can still have a successor. The
// iterator therefore counts DISTINCT cursor keys, which is the unit the
// server itself pages by, and it refuses to stop on anything but a page
// that did not fill.
//
//	it := client.UserTransactions(userID)
//	for it.Next(ctx) {
//	        txn := it.Item()
//	}
//	if err := it.Err(); err != nil { ... }
//
// An Iterator is not safe for concurrent use.
type Iterator[T any] struct {
	fetch  func(ctx context.Context, cursor uuid.UUID, limit int) ([]T, error)
	key    func(T) uuid.UUID
	limit  int
	cursor uuid.UUID

	page []T
	pos  int
	done bool
	err  error
}

func newIterator[T any](
	fetch func(ctx context.Context, cursor uuid.UUID, limit int) ([]T, error),
	key func(T) uuid.UUID,
) *Iterator[T] {
	return &Iterator[T]{fetch: fetch, key: key, limit: DefaultPageSize}
}

// PageSize asks for pages of n items (1..100). It must be called before the
// first Next.
func (it *Iterator[T]) PageSize(n int) *Iterator[T] {
	if n > 0 {
		it.limit = n
	}
	return it
}

// Next advances to the next item, fetching a page when needed. It returns
// false at the end of the listing and on the first error; check Err.
func (it *Iterator[T]) Next(ctx context.Context) bool {
	if it.err != nil {
		return false
	}
	if it.pos < len(it.page) {
		it.pos++
		return true
	}
	if it.done {
		return false
	}

	page, err := it.fetch(ctx, it.cursor, it.limit)
	if err != nil {
		it.err = err
		return false
	}
	if len(page) == 0 {
		it.done = true
		return false
	}

	distinct := make(map[uuid.UUID]struct{}, len(page))
	for _, item := range page {
		distinct[it.key(item)] = struct{}{}
	}
	next := it.key(page[len(page)-1])
	if len(distinct) < it.limit {
		// The server had nothing more to give: this is the last page.
		it.done = true
	} else if next == it.cursor {
		it.err = fmt.Errorf("%w (cursor %s)", ErrPaginationStalled, next)
		return false
	}
	it.cursor = next

	it.page, it.pos = page, 1
	return true
}

// Item is the current item. It is only defined after Next returned true.
func (it *Iterator[T]) Item() T { return it.page[it.pos-1] }

// Err is the error that stopped the walk, nil if it ran to the end.
func (it *Iterator[T]) Err() error { return it.err }

// All is the range-over-func form. The loop stops at the first error,
// which is yielded with the zero item; Err holds it afterwards too.
//
//	for txn, err := range client.UserTransactions(userID).All(ctx) {
//	        if err != nil { return err }
//	}
func (it *Iterator[T]) All(ctx context.Context) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for it.Next(ctx) {
			if !yield(it.Item(), nil) {
				return
			}
		}
		if it.err != nil {
			var zero T
			yield(zero, it.err)
		}
	}
}

// Collect walks the whole listing into a slice. Use it when the result set
// is known to be small; otherwise range it.
func (it *Iterator[T]) Collect(ctx context.Context) ([]T, error) {
	var out []T
	for it.Next(ctx) {
		out = append(out, it.Item())
	}
	return out, it.Err()
}

// pageIterator walks a listing that answers with an envelope carrying its
// own next_cursor, so no inference is needed: absent means last page.
type pageIterator[T any] struct {
	fetch  func(ctx context.Context, cursor uuid.UUID, limit int) ([]T, *uuid.UUID, error)
	limit  int
	cursor uuid.UUID

	page []T
	pos  int
	done bool
	err  error
}

func newPageIterator[T any](fetch func(ctx context.Context, cursor uuid.UUID, limit int) ([]T, *uuid.UUID, error)) *PageIterator[T] {
	return &PageIterator[T]{pageIterator[T]{fetch: fetch, limit: DefaultPageSize}}
}

// PageIterator walks a listing whose responses carry next_cursor.
type PageIterator[T any] struct{ it pageIterator[T] }

// PageSize asks for pages of n items (1..100), before the first Next.
func (p *PageIterator[T]) PageSize(n int) *PageIterator[T] {
	if n > 0 {
		p.it.limit = n
	}
	return p
}

// Next advances to the next item.
func (p *PageIterator[T]) Next(ctx context.Context) bool {
	it := &p.it
	if it.err != nil {
		return false
	}
	if it.pos < len(it.page) {
		it.pos++
		return true
	}
	if it.done {
		return false
	}

	page, next, err := it.fetch(ctx, it.cursor, it.limit)
	if err != nil {
		it.err = err
		return false
	}
	switch {
	case next == nil:
		it.done = true
	case *next == it.cursor && it.cursor != uuid.Nil:
		it.err = fmt.Errorf("%w (cursor %s)", ErrPaginationStalled, *next)
		return false
	default:
		it.cursor = *next
	}
	if len(page) == 0 {
		it.done = true
		return false
	}
	it.page, it.pos = page, 1
	return true
}

// Item is the current item, defined only after Next returned true.
func (p *PageIterator[T]) Item() T { return p.it.page[p.it.pos-1] }

// Err is the error that stopped the walk.
func (p *PageIterator[T]) Err() error { return p.it.err }

// All is the range-over-func form.
func (p *PageIterator[T]) All(ctx context.Context) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for p.Next(ctx) {
			if !yield(p.Item(), nil) {
				return
			}
		}
		if p.it.err != nil {
			var zero T
			yield(zero, p.it.err)
		}
	}
}

// Collect walks the whole listing into a slice.
func (p *PageIterator[T]) Collect(ctx context.Context) ([]T, error) {
	var out []T
	for p.Next(ctx) {
		out = append(out, p.Item())
	}
	return out, p.Err()
}
