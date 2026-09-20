package walletd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SignatureHeader is the header WalletD signs every delivery with.
const SignatureHeader = "X-Wallet-Signature"

// EventIDHeader carries the event id. It is the deduplication key for an
// at-least-once feed: record it and make reprocessing a no-op.
const EventIDHeader = "X-Wallet-Event-Id"

// DefaultTolerance is the timestamp skew the platform documents: five
// minutes either way.
const DefaultTolerance = 5 * time.Minute

// Signature verification failures. They are deliberately separate so a
// receiver can alarm on a forgery and merely log a clock problem, but a
// receiver must reject the delivery on any of them.
var (
	// ErrSignatureMalformed means the header is not t=<unix>,v1=<hex>.
	ErrSignatureMalformed = errors.New("malformed signature header")
	// ErrSignatureStale means the timestamp is outside the tolerance:
	// either a replayed capture or a badly skewed clock.
	ErrSignatureStale = errors.New("signature timestamp outside tolerance")
	// ErrSignatureMismatch means the body, the secret, or both are not
	// what was signed. Treat it as a forgery.
	ErrSignatureMismatch = errors.New("signature does not match")
)

// VerifySignature checks one delivery against its endpoint secret.
//
//	X-Wallet-Signature: t=<unix seconds>,v1=<hex hmac_sha256(secret, "<t>.<raw body>")>
//
// body must be the RAW bytes as received. Decoding the JSON and
// re-encoding it changes key order and whitespace, and therefore changes
// the hash: every such receiver rejects every genuine delivery. Read the
// body with io.ReadAll before anything else touches it.
//
// tolerance bounds how far the timestamp may be from now, in either
// direction; pass DefaultTolerance unless you have a reason. Without it a
// captured delivery can be replayed at any time in the future, which is the
// attack the timestamp exists to stop.
//
// A nil return means the delivery came from WalletD and has not been
// altered. It says nothing about whether you have already processed this
// event — that is what EventIDHeader is for.
//
//	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
//	if err != nil { ... }
//	if err := walletd.VerifySignature(r.Header.Get(walletd.SignatureHeader), body, secret, walletd.DefaultTolerance); err != nil {
//	        w.WriteHeader(http.StatusUnauthorized)
//	        return
//	}
//
// One secret belongs to one endpoint of one tenant. If your receiver serves
// several tenants, select the secret by the delivery's tenant_id; verifying
// tenant A's delivery against tenant B's secret fails exactly like a
// forgery, which is the point.
func VerifySignature(header string, body []byte, secret string, tolerance time.Duration) error {
	if secret == "" {
		return fmt.Errorf("%w: empty secret", ErrSignatureMismatch)
	}
	if tolerance < 0 {
		tolerance = -tolerance
	}
	return verifySignatureAt(header, body, secret, tolerance, time.Now())
}

// verifySignatureAt is VerifySignature with the clock injected, so the
// tolerance window itself is testable.
func verifySignatureAt(header string, body []byte, secret string, tolerance time.Duration, now time.Time) error {
	parts := strings.Split(header, ",")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || !strings.HasPrefix(parts[1], "v1=") {
		return ErrSignatureMalformed
	}
	seconds, err := strconv.ParseInt(parts[0][2:], 10, 64)
	if err != nil {
		return ErrSignatureMalformed
	}
	if _, err := hex.DecodeString(parts[1][3:]); err != nil {
		return ErrSignatureMalformed
	}

	// The timestamp is checked before the MAC so that a replay is reported
	// as a replay. Both paths reject, so the order leaks nothing.
	signedAt := time.Unix(seconds, 0)
	if signedAt.Before(now.Add(-tolerance)) || signedAt.After(now.Add(tolerance)) {
		return fmt.Errorf("%w: signed at %s, now %s", ErrSignatureStale,
			signedAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}

	// hmac.Equal, never ==: a byte-by-byte comparison returns sooner on an
	// earlier mismatch, and that timing difference is enough to recover a
	// valid signature one character at a time.
	if !hmac.Equal([]byte(sign(secret, seconds, body)), []byte(header)) {
		return ErrSignatureMismatch
	}
	return nil
}

// sign rebuilds the whole header, so the comparison covers the timestamp as
// well as the digest: a header whose t was edited after signing cannot
// match, whatever the hex says.
func sign(secret string, unixSeconds int64, body []byte) string {
	ts := strconv.FormatInt(unixSeconds, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// Event is one webhook delivery's envelope. Data holds the object the
// event is about, keyed by its kind ("topup", "payment", "order"); use
// Event.Into to decode it.
//
// Treat an event as an observation of something that already happened, not
// as an instruction, and ignore a Type you do not recognise: new ones are
// added without warning.
type Event struct {
	ID        uuid.UUID                  `json:"id"`
	Type      string                     `json:"type"`
	TenantID  uuid.UUID                  `json:"tenant_id"`
	ClientID  *uuid.UUID                 `json:"client_id,omitempty"`
	CreatedAt time.Time                  `json:"created_at"`
	Data      map[string]json.RawMessage `json:"data"`
}

// ParseEvent decodes a verified delivery body.
//
// Verify the signature FIRST. An unverified body is attacker-controlled
// input, and this function will happily decode a forgery.
func ParseEvent(body []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(body, &e); err != nil {
		return Event{}, fmt.Errorf("walletd: decode event: %w", err)
	}
	if e.Type == "" {
		return Event{}, fmt.Errorf("walletd: decode event: no type")
	}
	return e, nil
}

// Into decodes the named object of the event's payload into v, for example
//
//	var topup walletd.Topup
//	err := event.Into("topup", &topup)
//
// It reports whether the key was present.
func (e Event) Into(key string, v any) error {
	raw, ok := e.Data[key]
	if !ok {
		return fmt.Errorf("walletd: event %s has no %q payload", e.Type, key)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("walletd: decode event %s payload %q: %w", e.Type, key, err)
	}
	return nil
}
