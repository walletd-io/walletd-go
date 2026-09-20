package walletd

import (
	"fmt"

	"github.com/google/uuid"
)

// IdempotencyKey makes one money-moving call safe to repeat. The server
// stores the first answer under the key and replays it byte for byte for
// every later call with the same key, so a timeout, a crash between the
// request and the response, or an SDK retry costs at most one movement.
//
// Every method on Client that moves money takes one as an explicit
// parameter. That is deliberate: the only way to send a payment without a
// key is to not compile.
//
// The key belongs to the caller's logical operation, not to an HTTP
// attempt. Derive it from something the caller already has and will still
// have after a restart — an order number, an invoice line, a job id:
//
//	key := walletd.IdempotencyKey("order-8891-capture")
//
// NewIdempotencyKey is the fallback when there is no such identifier, and
// only then: a random key minted inside a retry loop, or regenerated after
// a crash, defeats the whole mechanism — the second attempt looks like a
// new payment and the customer is charged twice. Mint it once, persist it
// with the work, reuse it on every attempt.
//
// The contract accepts 1 to 200 characters. Reusing a key with different
// parameters is an idempotency_key_reuse conflict, not a replay.
type IdempotencyKey string

// NewIdempotencyKey mints a random key. Use it only where no natural
// identifier exists, and store it before the first attempt.
func NewIdempotencyKey() IdempotencyKey { return IdempotencyKey(uuid.NewString()) }

func (k IdempotencyKey) String() string { return string(k) }

// maxIdempotencyKeyLen is the contract's limit on the header.
const maxIdempotencyKeyLen = 200

func (k IdempotencyKey) validate() error {
	switch {
	case k == "":
		return fmt.Errorf("%w: idempotency key must not be empty", ErrInvalidRequest)
	case len(k) > maxIdempotencyKeyLen:
		return fmt.Errorf("%w: idempotency key is %d characters, the limit is %d",
			ErrInvalidRequest, len(k), maxIdempotencyKeyLen)
	}
	for _, r := range k {
		// The header must survive an HTTP round trip unmangled.
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("%w: idempotency key must be printable ASCII", ErrInvalidRequest)
		}
	}
	return nil
}
