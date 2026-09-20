package walletd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Problem, declared in types.gen.go from the contract's own schema, is the
// error every failed call returns. Its fields cannot drift from what the
// API documents, because they are generated from it.
//
// Branch on Code, never on Title or Detail: Code is the stable contract and
// the prose is not. errors.Is against the sentinels below is the same check
// spelled more safely.
//
//	if errors.Is(err, walletd.ErrInsufficientFunds) { ... }
//
//	var p *walletd.Problem
//	if errors.As(err, &p) && p.Code == walletd.CodeTierLimitExceeded { ... }
var _ error = (*Problem)(nil)

// Error renders the problem for a log line. It is not a branch target.
func (p *Problem) Error() string {
	detail := p.Message()
	if detail == "" {
		detail = p.Title
	}
	return fmt.Sprintf("walletd: %s (%d %s)", detail, p.Status, p.Code)
}

// Message is Detail when the server sent one, otherwise Title.
func (p *Problem) Message() string {
	if p.Detail != nil && *p.Detail != "" {
		return *p.Detail
	}
	return p.Title
}

// Unwrap exposes two sentinels: the one for this exact problem code, and
// the one for its status class. Both errors.Is checks work, so a caller can
// handle `insufficient_funds` precisely or every 422 generically without
// knowing which codes exist.
func (p *Problem) Unwrap() []error {
	var out []error
	if sentinel, ok := codeSentinels[p.Code]; ok {
		out = append(out, sentinel)
	}
	if class := statusSentinel(p.Status); class != nil && (len(out) == 0 || out[0] != class) {
		out = append(out, class)
	}
	return out
}

// Documented problem codes. The list mirrors the errors page of the
// developer documentation; a code the server adds later arrives as a plain
// string in Problem.Code and still matches its status class.
const (
	CodeUnauthenticated            = "unauthenticated"
	CodeUnknownTenant              = "unknown_tenant"
	CodeTenantSuspended            = "tenant_suspended"
	CodeInsufficientScope          = "insufficient_scope"
	CodeForbidden                  = "forbidden"
	CodeUserTokenRequired          = "user_token_required"
	CodeConsumerTokenRequired      = "consumer_token_required"
	CodePlatformScopeOnly          = "platform_scope_only"
	CodeNotClientMember            = "not_client_member"
	CodeInvalidRequest             = "invalid_request"
	CodeSelfTransfer               = "self_transfer"
	CodeIdempotencyKeyReuse        = "idempotency_key_reuse"
	CodeUserExists                 = "user_exists"
	CodeMerchantExists             = "merchant_exists"
	CodeRuleExists                 = "rule_exists"
	CodeInsufficientFunds          = "insufficient_funds"
	CodeInsufficientPoints         = "insufficient_points"
	CodeTierLimitExceeded          = "tier_limit_exceeded"
	CodeNoConversionRule           = "no_conversion_rule"
	CodeNothingToPay               = "nothing_to_pay"
	CodeNotAnItem                  = "not_an_item"
	CodeNotAPlan                   = "not_a_plan"
	CodeUserNotBusiness            = "user_not_business"
	CodeCreditLimitBelowBalance    = "credit_limit_below_balance"
	CodeUserNotFound               = "user_not_found"
	CodeSenderNotFound             = "sender_not_found"
	CodeRecipientNotFound          = "recipient_not_found"
	CodePayerNotFound              = "payer_not_found"
	CodeMerchantNotFound           = "merchant_not_found"
	CodePaymentNotFound            = "payment_not_found"
	CodeTopupNotFound              = "topup_not_found"
	CodeSubscriptionNotFound       = "subscription_not_found"
	CodeClientNotFound             = "client_not_found"
	CodeOfferingNotFound           = "offering_not_found"
	CodeRuleNotFound               = "rule_not_found"
	CodeEventNotFound              = "event_not_found"
	CodeTenantNotFound             = "tenant_not_found"
	CodeRateLimited                = "rate_limited"
	CodeBulkheadFull               = "bulkhead_full"
	CodeGatewayUnreachable         = "gateway_unreachable"
	CodeAuthUnavailable            = "auth_unavailable"
	CodeReportingTimeout           = "reporting_timeout"
	CodeOrgProvisioningUnavailable = "org_provisioning_unavailable"
	CodeInternalError              = "internal_error"
)

// Status-class sentinels. Every Problem matches exactly one of these.
var (
	ErrBadRequest    = errors.New("bad request")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrForbidden     = errors.New("forbidden")
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrUnprocessable = errors.New("refused by a money rule")
	ErrRateLimited   = errors.New("rate limited")
	ErrServer        = errors.New("server error")

	// ErrTransport is a request that never got a complete answer: DNS,
	// dial, TLS or a response read failure. Whether the call landed is unknown, which is
	// exactly what an idempotency key is for.
	ErrTransport = errors.New("transport failure")

	// ErrUnexpectedStatus indicates a non-2xx response outside the HTTP problem range.
	// Redirects are not followed; configure the API host directly.
	ErrUnexpectedStatus = errors.New("unexpected HTTP status")
)

// Problem-code sentinels for the documented codes.
var (
	ErrUnauthenticated       = errors.New("unauthenticated")
	ErrUnknownTenant         = errors.New("unknown tenant")
	ErrTenantSuspended       = errors.New("tenant suspended")
	ErrInsufficientScope     = errors.New("insufficient scope")
	ErrUserTokenRequired     = errors.New("user token required")
	ErrConsumerTokenRequired = errors.New("consumer token required")
	ErrPlatformScopeOnly     = errors.New("platform scope only")
	ErrNotClientMember       = errors.New("not a member of that client")

	ErrInvalidRequest      = errors.New("invalid request")
	ErrSelfTransfer        = errors.New("sender and recipient are the same wallet")
	ErrIdempotencyKeyReuse = errors.New("idempotency key reused with different parameters")
	ErrUserExists          = errors.New("user already exists")
	ErrMerchantExists      = errors.New("merchant already exists")
	ErrRuleExists          = errors.New("rule already exists")

	ErrInsufficientFunds       = errors.New("insufficient funds")
	ErrInsufficientPoints      = errors.New("insufficient points")
	ErrTierLimitExceeded       = errors.New("tier limit exceeded")
	ErrNoConversionRule        = errors.New("no active conversion rule")
	ErrNothingToPay            = errors.New("nothing to pay")
	ErrNotAnItem               = errors.New("offering is not a one-off item")
	ErrNotAPlan                = errors.New("offering is not a plan")
	ErrUserNotBusiness         = errors.New("user is not a business")
	ErrCreditLimitBelowBalance = errors.New("credit limit below drawn balance")

	ErrUserNotFound         = errors.New("user not found")
	ErrSenderNotFound       = errors.New("sender not found")
	ErrRecipientNotFound    = errors.New("recipient not found")
	ErrPayerNotFound        = errors.New("payer not found")
	ErrMerchantNotFound     = errors.New("merchant not found")
	ErrPaymentNotFound      = errors.New("payment not found")
	ErrTopupNotFound        = errors.New("topup not found")
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrClientNotFound       = errors.New("client not found")
	ErrOfferingNotFound     = errors.New("offering not found")
	ErrRuleNotFound         = errors.New("rule not found")
	ErrEventNotFound        = errors.New("event not found")
	ErrTenantNotFound       = errors.New("tenant not found")

	ErrBulkheadFull               = errors.New("gateway concurrency compartment full")
	ErrGatewayUnreachable         = errors.New("payment gateway unreachable")
	ErrAuthUnavailable            = errors.New("credential service unavailable")
	ErrReportingTimeout           = errors.New("reporting window too wide")
	ErrOrgProvisioningUnavailable = errors.New("identity provisioning unavailable")
	ErrInternal                   = errors.New("internal error")
)

var codeSentinels = map[string]error{
	CodeUnauthenticated:            ErrUnauthenticated,
	CodeUnknownTenant:              ErrUnknownTenant,
	CodeTenantSuspended:            ErrTenantSuspended,
	CodeInsufficientScope:          ErrInsufficientScope,
	CodeForbidden:                  ErrForbidden,
	CodeUserTokenRequired:          ErrUserTokenRequired,
	CodeConsumerTokenRequired:      ErrConsumerTokenRequired,
	CodePlatformScopeOnly:          ErrPlatformScopeOnly,
	CodeNotClientMember:            ErrNotClientMember,
	CodeInvalidRequest:             ErrInvalidRequest,
	CodeSelfTransfer:               ErrSelfTransfer,
	CodeIdempotencyKeyReuse:        ErrIdempotencyKeyReuse,
	CodeUserExists:                 ErrUserExists,
	CodeMerchantExists:             ErrMerchantExists,
	CodeRuleExists:                 ErrRuleExists,
	CodeInsufficientFunds:          ErrInsufficientFunds,
	CodeInsufficientPoints:         ErrInsufficientPoints,
	CodeTierLimitExceeded:          ErrTierLimitExceeded,
	CodeNoConversionRule:           ErrNoConversionRule,
	CodeNothingToPay:               ErrNothingToPay,
	CodeNotAnItem:                  ErrNotAnItem,
	CodeNotAPlan:                   ErrNotAPlan,
	CodeUserNotBusiness:            ErrUserNotBusiness,
	CodeCreditLimitBelowBalance:    ErrCreditLimitBelowBalance,
	CodeUserNotFound:               ErrUserNotFound,
	CodeSenderNotFound:             ErrSenderNotFound,
	CodeRecipientNotFound:          ErrRecipientNotFound,
	CodePayerNotFound:              ErrPayerNotFound,
	CodeMerchantNotFound:           ErrMerchantNotFound,
	CodePaymentNotFound:            ErrPaymentNotFound,
	CodeTopupNotFound:              ErrTopupNotFound,
	CodeSubscriptionNotFound:       ErrSubscriptionNotFound,
	CodeClientNotFound:             ErrClientNotFound,
	CodeOfferingNotFound:           ErrOfferingNotFound,
	CodeRuleNotFound:               ErrRuleNotFound,
	CodeEventNotFound:              ErrEventNotFound,
	CodeTenantNotFound:             ErrTenantNotFound,
	CodeRateLimited:                ErrRateLimited,
	CodeBulkheadFull:               ErrBulkheadFull,
	CodeGatewayUnreachable:         ErrGatewayUnreachable,
	CodeAuthUnavailable:            ErrAuthUnavailable,
	CodeReportingTimeout:           ErrReportingTimeout,
	CodeOrgProvisioningUnavailable: ErrOrgProvisioningUnavailable,
	CodeInternalError:              ErrInternal,
}

func statusSentinel(status int) error {
	switch {
	case status == http.StatusBadRequest:
		return ErrBadRequest
	case status == http.StatusUnauthorized:
		return ErrUnauthorized
	case status == http.StatusForbidden:
		return ErrForbidden
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusConflict:
		return ErrConflict
	case status == http.StatusUnprocessableEntity:
		return ErrUnprocessable
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status >= 500:
		return ErrServer
	default:
		return nil
	}
}

// AsProblem extracts the problem from an error returned by any Client
// method, false if the call failed before or below HTTP.
func AsProblem(err error) (*Problem, bool) {
	var p *Problem
	if errors.As(err, &p) {
		return p, true
	}
	return nil, false
}

// CodeOf returns the problem code, or "" for a non-HTTP failure.
func CodeOf(err error) string {
	if p, ok := AsProblem(err); ok {
		return p.Code
	}
	return ""
}

// decodeProblem turns an error response into a *Problem. A body that is not
// problem+json — a proxy's HTML error page, an empty 502 — still yields a
// Problem carrying the status, so callers never have to handle two error
// shapes. The caller closes the body.
func decodeProblem(resp *http.Response) *Problem {
	p := &Problem{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(p); err != nil {
		p = &Problem{}
	}
	if p.Title == "" {
		p.Title = http.StatusText(resp.StatusCode)
		if p.Title == "" {
			p.Title = resp.Status
		}
	}
	// The status in the envelope is advisory; the one on the wire is what
	// happened. A mismatch would send a caller down the wrong branch.
	p.Status = resp.StatusCode
	return p
}
