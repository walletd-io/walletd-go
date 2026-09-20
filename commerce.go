package walletd

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"
)

// The catalog and order surfaces are read-only in v0.1 of this SDK: enough
// to render a storefront and reconcile orders, not to run a merchant
// back office. Writing to the catalog goes through the merchant portal or
// plain HTTP until the surface settles.

// ListClients returns the tenant's catalog clients (merchant storefronts).
//
// The contract gives this listing a limit but no cursor, so it returns at
// most limit clients, 100 at the ceiling, and cannot be paged.
func (c *Client) ListClients(ctx context.Context, status CatalogClientStatus, limit int) ([]CatalogClient, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", string(status))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []CatalogClient
	err := c.do(ctx, http.MethodGet, "/v1/clients", "", q, nil, &out)
	return out, err
}

// GetClient reads one catalog client.
func (c *Client) GetClient(ctx context.Context, clientID uuid.UUID) (CatalogClient, error) {
	var out CatalogClient
	err := c.do(ctx, http.MethodGet, "/v1/clients/"+clientID.String(), "", nil, nil, &out)
	return out, err
}

// OfferingFilter narrows a catalog listing. The zero value is everything.
type OfferingFilter struct {
	Status     string
	CategoryID *uuid.UUID
	Query      string
	LowStock   bool
}

func (f OfferingFilter) values() url.Values {
	q := url.Values{}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	if f.CategoryID != nil {
		q.Set("category_id", f.CategoryID.String())
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.LowStock {
		q.Set("low_stock", "true")
	}
	return q
}

// ListClientOfferings reads one page of a client's catalog. Unlike the
// other listings this one answers with an envelope that names the next
// cursor, so the last page is stated rather than inferred.
func (c *Client) ListClientOfferings(ctx context.Context, clientID uuid.UUID, filter OfferingFilter, cursor uuid.UUID, limit int) (OfferingPage, error) {
	q := filter.values()
	for k, v := range pageQuery(cursor, limit) {
		q[k] = v
	}
	var out OfferingPage
	err := c.do(ctx, http.MethodGet, "/v1/clients/"+clientID.String()+"/offerings", "", q, nil, &out)
	return out, err
}

// ClientOfferings iterates a client's whole catalog.
func (c *Client) ClientOfferings(clientID uuid.UUID, filter OfferingFilter) *PageIterator[Offering] {
	return newPageIterator(func(ctx context.Context, cursor uuid.UUID, limit int) ([]Offering, *uuid.UUID, error) {
		page, err := c.ListClientOfferings(ctx, clientID, filter, cursor, limit)
		if err != nil {
			return nil, nil, err
		}
		return page.Offerings, page.NextCursor, nil
	})
}

// GetClientOffering reads one offering with its variants.
func (c *Client) GetClientOffering(ctx context.Context, clientID, offeringID uuid.UUID) (Offering, error) {
	var out Offering
	err := c.do(ctx, http.MethodGet, "/v1/clients/"+clientID.String()+"/offerings/"+offeringID.String(), "", nil, nil, &out)
	return out, err
}

// ListOrders reads one page of the calling user's orders. Prefer Orders.
func (c *Client) ListOrders(ctx context.Context, cursor uuid.UUID, limit int) ([]Order, error) {
	var out []Order
	err := c.do(ctx, http.MethodGet, "/v1/orders", "", pageQuery(cursor, limit), nil, &out)
	return out, err
}

// Orders iterates the calling user's orders, newest first.
func (c *Client) Orders() *Iterator[Order] {
	return newIterator(
		func(ctx context.Context, cursor uuid.UUID, limit int) ([]Order, error) {
			return c.ListOrders(ctx, cursor, limit)
		},
		func(o Order) uuid.UUID { return o.Id },
	)
}

// GetOrder reads one order with its lines.
func (c *Client) GetOrder(ctx context.Context, orderID uuid.UUID) (Order, error) {
	var out Order
	err := c.do(ctx, http.MethodGet, "/v1/orders/"+orderID.String(), "", nil, nil, &out)
	return out, err
}

// ListExploreClients reads one page of the shopper-facing directory of
// approved storefronts. Prefer ExploreClients.
func (c *Client) ListExploreClients(ctx context.Context, query, category string, cursor uuid.UUID, limit int) ([]CatalogClient, error) {
	q := pageQuery(cursor, limit)
	if query != "" {
		q.Set("q", query)
	}
	if category != "" {
		q.Set("category", category)
	}
	var out []CatalogClient
	err := c.do(ctx, http.MethodGet, "/v1/explore/clients", "", q, nil, &out)
	return out, err
}

// ExploreClients iterates the shopper-facing storefront directory.
func (c *Client) ExploreClients(query, category string) *Iterator[CatalogClient] {
	return newIterator(
		func(ctx context.Context, cursor uuid.UUID, limit int) ([]CatalogClient, error) {
			return c.ListExploreClients(ctx, query, category, cursor, limit)
		},
		func(cl CatalogClient) uuid.UUID { return cl.Id },
	)
}

// ExploreClient reads one storefront as a shopper sees it.
func (c *Client) ExploreClient(ctx context.Context, clientID uuid.UUID) (ExploreClientDetail, error) {
	var out ExploreClientDetail
	err := c.do(ctx, http.MethodGet, "/v1/explore/clients/"+clientID.String(), "", nil, nil, &out)
	return out, err
}

// ExploreOffering reads one offering as a shopper sees it.
func (c *Client) ExploreOffering(ctx context.Context, offeringID uuid.UUID) (ExploreOfferingDetail, error) {
	var out ExploreOfferingDetail
	err := c.do(ctx, http.MethodGet, "/v1/explore/offerings/"+offeringID.String(), "", nil, nil, &out)
	return out, err
}
