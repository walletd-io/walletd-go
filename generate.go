// Package walletd is the first-party Go SDK for the WalletD API: contract
// types generated from the published OpenAPI document, one HTTP client that
// carries credentials, idempotency keys and retries, RFC 7807 problems
// decoded into errors callers can errors.Is against, and webhook signature
// verification.
package walletd

// api/openapi.yaml is a byte copy of the published contract served at
// https://docs.walletd.io/openapi.yaml. Regenerate after replacing it; the
// SDK cannot drift from the contract without this step.
//go:generate go tool oapi-codegen -config config.yaml api/openapi.yaml
