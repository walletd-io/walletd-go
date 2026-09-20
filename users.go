package walletd

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"
)

// pageQuery builds the cursor/limit pair the listings share. The limit is
// always sent, never left to the server's default, because the iterators
// decide the end of a listing by comparing the page against the limit they
// asked for.
func pageQuery(cursor uuid.UUID, limit int) url.Values {
	q := url.Values{}
	if cursor != uuid.Nil {
		q.Set("cursor", cursor.String())
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return q
}

// Me returns the user the credential resolves to. It is the call a mobile
// app makes with a user token; an API key has no user and gets a problem.
func (c *Client) Me(ctx context.Context) (User, error) {
	var out User
	err := c.do(ctx, http.MethodGet, "/v1/me", "", nil, nil, &out)
	return out, err
}

// CreateUser mirrors one of your users into the wallet. ExternalId is
// yours and unique per tenant, so creating the same one twice is a
// user_exists conflict.
func (c *Client) CreateUser(ctx context.Context, req CreateUserRequest) (User, error) {
	var out User
	err := c.do(ctx, http.MethodPost, "/v1/users", "", nil, req, &out)
	return out, err
}

// GetUser reads one user.
func (c *Client) GetUser(ctx context.Context, userID uuid.UUID) (User, error) {
	var out User
	err := c.do(ctx, http.MethodGet, "/v1/users/"+userID.String(), "", nil, nil, &out)
	return out, err
}

// ListUsers reads one page. Prefer Users, which pages correctly on its own.
// query matches the display name, handle or external id.
func (c *Client) ListUsers(ctx context.Context, query string, cursor uuid.UUID, limit int) ([]User, error) {
	q := pageQuery(cursor, limit)
	if query != "" {
		q.Set("query", query)
	}
	var out []User
	err := c.do(ctx, http.MethodGet, "/v1/users", "", q, nil, &out)
	return out, err
}

// Users iterates every user, newest first.
func (c *Client) Users(query string) *Iterator[User] {
	return newIterator(
		func(ctx context.Context, cursor uuid.UUID, limit int) ([]User, error) {
			return c.ListUsers(ctx, query, cursor, limit)
		},
		func(u User) uuid.UUID { return u.Id },
	)
}

// UserBalances returns one balance per purpose the user holds. Show
// Available, not Balance: the difference is money reserved by an
// uncaptured authorization.
func (c *Client) UserBalances(ctx context.Context, userID uuid.UUID) ([]UserBalance, error) {
	var out []UserBalance
	err := c.do(ctx, http.MethodGet, "/v1/users/"+userID.String()+"/balances", "", nil, nil, &out)
	return out, err
}

// ListUserTransactions reads one page of a user's history, newest first.
//
// The limit counts TRANSACTIONS, not rows: one transaction can put several
// entries in this list for the same user, so a page may be longer than the
// limit. Use UserTransactions unless you are paging by hand, and if you are,
// take the cursor from the last row's TxnId and stop only when the page
// holds fewer than `limit` distinct TxnIds.
func (c *Client) ListUserTransactions(ctx context.Context, userID, cursor uuid.UUID, limit int) ([]UserTransaction, error) {
	var out []UserTransaction
	err := c.do(ctx, http.MethodGet, "/v1/users/"+userID.String()+"/transactions", "", pageQuery(cursor, limit), nil, &out)
	return out, err
}

// UserTransactions iterates a user's whole history, newest first.
func (c *Client) UserTransactions(userID uuid.UUID) *Iterator[UserTransaction] {
	return newIterator(
		func(ctx context.Context, cursor uuid.UUID, limit int) ([]UserTransaction, error) {
			return c.ListUserTransactions(ctx, userID, cursor, limit)
		},
		func(t UserTransaction) uuid.UUID { return t.TxnId },
	)
}

// ListTransactions reads one page of the tenant's ledger transactions.
// txnType filters to one kind ("transfer", "payment", ...); empty is all.
func (c *Client) ListTransactions(ctx context.Context, txnType string, cursor uuid.UUID, limit int) ([]Transaction, error) {
	q := pageQuery(cursor, limit)
	if txnType != "" {
		q.Set("type", txnType)
	}
	var out []Transaction
	err := c.do(ctx, http.MethodGet, "/v1/transactions", "", q, nil, &out)
	return out, err
}

// Transactions iterates the tenant's ledger transactions, newest first.
func (c *Client) Transactions(txnType string) *Iterator[Transaction] {
	return newIterator(
		func(ctx context.Context, cursor uuid.UUID, limit int) ([]Transaction, error) {
			return c.ListTransactions(ctx, txnType, cursor, limit)
		},
		func(t Transaction) uuid.UUID { return t.Id },
	)
}

// SetUserCreditLimit sets the floor the user's cash balance may go below
// zero, absolutely — it is not a delta. A limit under what the account has
// already drawn is refused with credit_limit_below_balance.
//
// This changes what the user may spend but moves no money, which is why the
// contract does not key it.
func (c *Client) SetUserCreditLimit(ctx context.Context, userID uuid.UUID, req SetCreditLimitRequest) (CreditLine, error) {
	var out CreditLine
	err := c.do(ctx, http.MethodPost, "/v1/users/"+userID.String()+"/credit_limit", "", nil, req, &out)
	return out, err
}
