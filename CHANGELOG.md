# Changelog

## v0.1.2

WalletD Go SDK v0.1.2 targets the existing WalletD API 0.8.0 snapshot.

- Retry interrupted success-response reads for GET/HEAD and keyed writes using the original request bytes and idempotency key. Exhaustion matches ErrTransport; schema errors do not retry, and failed attempts never expose partial results.
- Accept only 2xx success responses. Redirects are always refused, including with a supplied HTTP client, which is copied. Unexpected non-2xx responses outside 4xx/5xx match ErrUnexpectedStatus.
- Validate nonzero webhook event and tenant IDs after signature verification. Deduplicate using the signed body, not the unsigned event-ID header; examples acknowledge only after durable inbox acceptance.

Compatibility: callers relying on automatic redirects must configure the final API host. ParseEvent now rejects incomplete identities. Public method signatures and API contract types are unchanged. The release includes the v0.1.1 pagination bounds fix.

Validation: Go race tests, build, vet (including smoke-tag compilation), golangci-lint and generated-type freshness checks. Sandbox integration results are recorded separately; these unit checks do not establish production readiness.
