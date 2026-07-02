package linkbridge

import (
	"context"

	"github.com/linkbridge-systems/linkbridge-sdks/sdk-go/oapi"
)

// InvoicesAPI groups invoice-related operations.
type InvoicesAPI struct{ c *Client }

// SubmitOptions tunes a single Submit call.
type SubmitOptions struct {
	// IdempotencyKey is required by the API. If empty, the SDK
	// generates a fresh one via IdempotencyKey().
	IdempotencyKey string
	// Mode mirrors the ?mode= query param (sync | async | dry_run).
	// When zero-valued the server applies its default.
	Mode string
}

// Submit POSTs an invoice. The payload type is the canonical
// (additionalProperties: true) Invoice — build it from
// `packages/schema/invoice.schema.json` or use the helper builders in
// downstream packages.
//
// Returns the InvoiceAccepted (202) envelope on success. For sync mode
// the API may instead return InvoiceResult (200); use Raw().PostV1Invoices
// when callers need that branch.
func (a *InvoicesAPI) Submit(ctx context.Context, payload oapi.Invoice, opts SubmitOptions) (*oapi.InvoiceAccepted, error) {
	if opts.IdempotencyKey == "" {
		opts.IdempotencyKey = IdempotencyKey()
	}
	params := &oapi.PostV1InvoicesParams{IdempotencyKey: opts.IdempotencyKey}
	if opts.Mode != "" {
		mode := oapi.PostV1InvoicesParamsMode(opts.Mode)
		params.Mode = &mode
	}
	resp, err := a.c.raw.PostV1Invoices(ctx, params, payload)
	if err != nil {
		return nil, err
	}
	parsed, err := oapi.ParsePostV1InvoicesResponse(resp)
	if err != nil {
		return nil, err
	}
	if parsed.JSON202 != nil {
		return parsed.JSON202, nil
	}
	if parsed.JSON200 != nil {
		// sync-mode success — translate to InvoiceAccepted shape so
		// callers don't need to special-case the rare path.
		return &oapi.InvoiceAccepted{
			Irn:         parsed.JSON200.Irn,
			Status:      parsed.JSON200.Status,
			TrackingUrl: "/v1/invoices/" + parsed.JSON200.Irn,
		}, nil
	}
	return nil, decodeAPIError(parsed.HTTPResponse.StatusCode, parsed.Body)
}

// Get fetches a single invoice by IRN.
func (a *InvoicesAPI) Get(ctx context.Context, irn string) (*oapi.InvoiceRecord, error) {
	resp, err := a.c.raw.GetV1InvoicesIrn(ctx, irn)
	if err != nil {
		return nil, err
	}
	parsed, err := oapi.ParseGetV1InvoicesIrnResponse(resp)
	if err != nil {
		return nil, err
	}
	if parsed.JSON200 != nil {
		return parsed.JSON200, nil
	}
	return nil, decodeAPIError(parsed.HTTPResponse.StatusCode, parsed.Body)
}

// ListOptions configures the paginated invoice listing.
type ListOptions struct {
	Cursor string
	Limit  int
	Status string
}

// List returns one page of invoices for the authenticated tenant.
func (a *InvoicesAPI) List(ctx context.Context, opts ListOptions) (*oapi.InvoicePage, error) {
	params := &oapi.GetV1InvoicesParams{}
	if opts.Cursor != "" {
		params.Cursor = &opts.Cursor
	}
	if opts.Limit > 0 {
		params.Limit = &opts.Limit
	}
	if opts.Status != "" {
		s := oapi.GetV1InvoicesParamsStatus(opts.Status)
		params.Status = &s
	}
	resp, err := a.c.raw.GetV1Invoices(ctx, params)
	if err != nil {
		return nil, err
	}
	parsed, err := oapi.ParseGetV1InvoicesResponse(resp)
	if err != nil {
		return nil, err
	}
	if parsed.JSON200 != nil {
		return parsed.JSON200, nil
	}
	return nil, decodeAPIError(parsed.HTTPResponse.StatusCode, parsed.Body)
}

// WebhooksAPI groups webhook-related operations.
type WebhooksAPI struct{ c *Client }

// Create registers a new webhook subscription. The plaintext secret is
// returned on the response and never again — store it securely.
func (a *WebhooksAPI) Create(ctx context.Context, body oapi.WebhookCreate) (*oapi.Webhook, error) {
	resp, err := a.c.raw.PostV1Webhooks(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := readAll(resp)
		return nil, decodeAPIError(resp.StatusCode, raw)
	}
	var wh oapi.Webhook
	if err := decodeJSON(resp, &wh); err != nil {
		return nil, err
	}
	return &wh, nil
}

// List returns all webhook subscriptions for the authenticated tenant.
func (a *WebhooksAPI) List(ctx context.Context) (*oapi.WebhookList, error) {
	resp, err := a.c.raw.GetV1Webhooks(ctx)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := readAll(resp)
		return nil, decodeAPIError(resp.StatusCode, raw)
	}
	var list oapi.WebhookList
	if err := decodeJSON(resp, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// LookupsAPI groups reference-data endpoints (HSN codes, tax codes).
type LookupsAPI struct{ c *Client }

// TaxCodes returns the tax-code reference list. The server supports
// If-None-Match for cheap revalidation; pass via etag.
func (a *LookupsAPI) TaxCodes(ctx context.Context, etag string) ([]oapi.TaxCode, string, error) {
	params := &oapi.GetV1LookupsTaxCodesParams{}
	if etag != "" {
		params.IfNoneMatch = &etag
	}
	resp, err := a.c.raw.GetV1LookupsTaxCodes(ctx, params)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	newEtag := resp.Header.Get("ETag")
	if resp.StatusCode == 304 {
		return nil, newEtag, nil
	}
	if resp.StatusCode/100 != 2 {
		raw, _ := readAll(resp)
		return nil, "", decodeAPIError(resp.StatusCode, raw)
	}
	var page struct {
		Items []oapi.TaxCode `json:"items"`
	}
	if err := decodeJSON(resp, &page); err != nil {
		return nil, "", err
	}
	return page.Items, newEtag, nil
}

// HSNCodesOptions filters and paginates the HSN catalogue.
type HSNCodesOptions struct {
	Q      string // free-text substring match on description
	Prefix string // code prefix, e.g. "85"
	Parent string // browse children of a node
	Cursor string // forward-only cursor from a prior page's NextCursor
	Limit  int    // page size (server default applies when zero)
	ETag   string // If-None-Match for conditional GET
}

// HSNCodes returns one page of the HSN product-code catalogue. The
// returned string is the response ETag; on a 304 the page is nil and the
// etag is unchanged — pass it back via opts.ETag for cheap revalidation.
func (a *LookupsAPI) HSNCodes(ctx context.Context, opts HSNCodesOptions) (*oapi.HsnCodePage, string, error) {
	params := &oapi.GetV1LookupsHsnCodesParams{}
	if opts.Q != "" {
		params.Q = &opts.Q
	}
	if opts.Prefix != "" {
		params.Prefix = &opts.Prefix
	}
	if opts.Parent != "" {
		params.Parent = &opts.Parent
	}
	if opts.Cursor != "" {
		params.Cursor = &opts.Cursor
	}
	if opts.Limit > 0 {
		params.Limit = &opts.Limit
	}
	if opts.ETag != "" {
		params.IfNoneMatch = &opts.ETag
	}
	resp, err := a.c.raw.GetV1LookupsHsnCodes(ctx, params)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	newEtag := resp.Header.Get("ETag")
	if resp.StatusCode == 304 {
		return nil, newEtag, nil
	}
	if resp.StatusCode/100 != 2 {
		raw, _ := readAll(resp)
		return nil, "", decodeAPIError(resp.StatusCode, raw)
	}
	var page oapi.HsnCodePage
	if err := decodeJSON(resp, &page); err != nil {
		return nil, "", err
	}
	return &page, newEtag, nil
}
