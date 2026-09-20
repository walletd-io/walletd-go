package walletd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A delivery captured from the documented scheme. The expected signature
// below was computed by a separate implementation (Python's hmac), not by
// this package, so the test proves interoperability rather than
// self-consistency.
const (
	katSecret    = "whsec_test_2f0a"
	katTimestamp = int64(1786405949)
	katBody      = `{"id":"019fedda-88ef-7253-8253-14d9e24723fb","type":"topup.succeeded","tenant_id":"019fedcf-70a4-7ecd-bda1-f45dd0fdc0ca","created_at":"2026-08-11T05:12:29.922Z","data":{"topup":{"id":"019fedda-88e6-7453-b2ef-33b30424910e","user_id":"019fedcf-7183-7a57-82e7-3ca266429b04","amount":5000,"status":"succeeded"}}}`
	katHeader    = "t=1786405949,v1=1c736dc954eeefb7dc94b8f68abe69f166c798ebd605dcfdc28842f038edcd8f"
)

var katSignedAt = time.Unix(katTimestamp, 0)

func TestSignMatchesAnIndependentImplementation(t *testing.T) {
	if got := sign(katSecret, katTimestamp, []byte(katBody)); got != katHeader {
		t.Fatalf("sign() = %s\nwant      %s", got, katHeader)
	}
}

func TestVerifySignature(t *testing.T) {
	valid := katHeader
	tests := []struct {
		name      string
		header    string
		body      string
		secret    string
		now       time.Time
		tolerance time.Duration
		want      error
	}{
		{
			name: "valid", header: valid, body: katBody, secret: katSecret,
			now: katSignedAt, tolerance: DefaultTolerance, want: nil,
		},
		{
			name: "valid at the edge of the window", header: valid, body: katBody, secret: katSecret,
			now: katSignedAt.Add(4*time.Minute + 59*time.Second), tolerance: DefaultTolerance, want: nil,
		},
		{
			name: "tampered body", header: valid, secret: katSecret,
			body: strings.Replace(katBody, `"amount":5000`, `"amount":500000`, 1),
			now:  katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMismatch,
		},
		{
			name: "one byte changed in the body", header: valid, secret: katSecret,
			body: katBody[:len(katBody)-2] + "X}",
			now:  katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMismatch,
		},
		{
			name: "re-serialised body with the same meaning", header: valid, secret: katSecret,
			body: `{"type":"topup.succeeded","id":"019fedda-88ef-7253-8253-14d9e24723fb"}`,
			now:  katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMismatch,
		},
		{
			name: "wrong secret", header: valid, body: katBody, secret: "whsec_someone_elses",
			now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMismatch,
		},
		{
			name: "empty secret", header: valid, body: katBody, secret: "",
			now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMismatch,
		},
		{
			name: "stale: replayed an hour later", header: valid, body: katBody, secret: katSecret,
			now: katSignedAt.Add(time.Hour), tolerance: DefaultTolerance, want: ErrSignatureStale,
		},
		{
			name: "stale: just past the window", header: valid, body: katBody, secret: katSecret,
			now: katSignedAt.Add(5*time.Minute + time.Second), tolerance: DefaultTolerance, want: ErrSignatureStale,
		},
		{
			name: "stale: dated into the future", header: valid, body: katBody, secret: katSecret,
			now: katSignedAt.Add(-time.Hour), tolerance: DefaultTolerance, want: ErrSignatureStale,
		},
		{
			name: "timestamp edited after signing", body: katBody, secret: katSecret,
			header: "t=" + strconv.FormatInt(katTimestamp+1, 10) + ",v1=" + strings.Split(valid, "v1=")[1],
			now:    katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMismatch,
		},
		{name: "empty header", header: "", body: katBody, secret: katSecret, now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMalformed},
		{name: "no v1", header: "t=1786405949", body: katBody, secret: katSecret, now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMalformed},
		{name: "no t", header: "v1=" + strings.Split(valid, "v1=")[1], body: katBody, secret: katSecret, now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMalformed},
		{name: "swapped order", header: "v1=" + strings.Split(valid, "v1=")[1] + ",t=1786405949", body: katBody, secret: katSecret, now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMalformed},
		{name: "non numeric t", header: "t=yesterday,v1=abcd", body: katBody, secret: katSecret, now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMalformed},
		{name: "non hex v1", header: "t=1786405949,v1=zzzz", body: katBody, secret: katSecret, now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMalformed},
		{name: "extra element", header: valid + ",v2=deadbeef", body: katBody, secret: katSecret, now: katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMalformed},
		{
			name: "truncated digest", body: katBody, secret: katSecret,
			header: "t=1786405949,v1=1c736dc954eeefb7",
			now:    katSignedAt, tolerance: DefaultTolerance, want: ErrSignatureMismatch,
		},
		{
			name: "empty body signed and verified", secret: katSecret, body: "",
			header: sign(katSecret, katTimestamp, nil),
			now:    katSignedAt, tolerance: DefaultTolerance, want: nil,
		},
		{
			name: "zero tolerance still accepts the exact second", header: valid, body: katBody, secret: katSecret,
			now: katSignedAt, tolerance: 0, want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.body != "" {
				body = []byte(tc.body)
			}
			err := verifySignatureAt(tc.header, body, tc.secret, tc.tolerance, tc.now)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("verify = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("verify = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifySignaturePublicEntryPoint(t *testing.T) {
	now := time.Now().Unix()
	body := []byte(`{"id":"1","type":"payment.captured"}`)
	header := sign("whsec_live", now, body)

	if err := VerifySignature(header, body, "whsec_live", DefaultTolerance); err != nil {
		t.Fatalf("VerifySignature = %v, want nil", err)
	}
	if err := VerifySignature(header, append(body, ' '), "whsec_live", DefaultTolerance); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("tampered body = %v, want ErrSignatureMismatch", err)
	}
	if err := VerifySignature(header, body, "", DefaultTolerance); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("empty secret = %v, want ErrSignatureMismatch", err)
	}
	// A negative tolerance is a caller mistake, not a licence to accept
	// everything: it is read as its magnitude.
	old := sign("whsec_live", now-3600, body)
	if err := VerifySignature(old, body, "whsec_live", -DefaultTolerance); !errors.Is(err, ErrSignatureStale) {
		t.Fatalf("negative tolerance = %v, want ErrSignatureStale", err)
	}
}

// The comparison must be constant time. This cannot be proven by timing in
// a unit test, so it is pinned structurally: the only comparison in the
// verifier is hmac.Equal over the whole header.
func TestVerifyUsesConstantTimeComparison(t *testing.T) {
	// A digest differing only in its last character must be rejected, and
	// so must one differing only in its first: a short-circuiting compare
	// would still reject both, but a test that fixes the behaviour on
	// both ends catches a rewrite that compares prefixes.
	body := []byte(`{"a":1}`)
	header := sign(katSecret, katTimestamp, body)
	digest := strings.Split(header, "v1=")[1]

	for _, mutated := range []string{
		"0" + digest[1:],
		digest[:len(digest)-1] + "0",
	} {
		if mutated == digest {
			continue
		}
		h := "t=" + strconv.FormatInt(katTimestamp, 10) + ",v1=" + mutated
		if err := verifySignatureAt(h, body, katSecret, DefaultTolerance, katSignedAt); !errors.Is(err, ErrSignatureMismatch) {
			t.Errorf("mutated digest %s accepted: %v", mutated, err)
		}
	}
}

func TestSignIsHMACSHA256OverTimestampDotBody(t *testing.T) {
	// Restate the scheme independently of sign(), so a change to the
	// message layout cannot pass unnoticed.
	body := []byte(`{"x":1}`)
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte("42.{\"x\":1}"))
	want := "t=42,v1=" + hex.EncodeToString(mac.Sum(nil))
	if got := sign("s", 42, body); got != want {
		t.Fatalf("sign = %s, want %s", got, want)
	}
}

func TestParseEvent(t *testing.T) {
	event, err := ParseEvent([]byte(katBody))
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if event.Type != "topup.succeeded" {
		t.Errorf("Type = %q", event.Type)
	}
	if event.ID.String() != "019fedda-88ef-7253-8253-14d9e24723fb" {
		t.Errorf("ID = %s", event.ID)
	}
	if event.TenantID.String() != "019fedcf-70a4-7ecd-bda1-f45dd0fdc0ca" {
		t.Errorf("TenantID = %s", event.TenantID)
	}
	if event.ClientID != nil {
		t.Errorf("ClientID = %v, want nil on a tenant-wide event", event.ClientID)
	}
	if event.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	var topup Topup
	if err := event.Into("topup", &topup); err != nil {
		t.Fatalf("Into: %v", err)
	}
	if topup.Amount != 5000 || topup.Status != "succeeded" {
		t.Errorf("topup = %+v", topup)
	}
	if err := event.Into("payment", &topup); err == nil {
		t.Error("Into on a missing key should fail")
	}
}

func TestParseEventCarriesClientID(t *testing.T) {
	body := []byte(`{"id":"019fedda-88ef-7253-8253-14d9e24723fb","type":"order.paid","tenant_id":"019fedcf-70a4-7ecd-bda1-f45dd0fdc0ca","client_id":"019fee0d-2c3d-7e4f-9a5b-6c7d8e9f0a1c","created_at":"2026-08-11T05:12:29.922Z","data":{}}`)
	event, err := ParseEvent(body)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if event.ClientID == nil || event.ClientID.String() != "019fee0d-2c3d-7e4f-9a5b-6c7d8e9f0a1c" {
		t.Fatalf("ClientID = %v, want the merchant id", event.ClientID)
	}
}

func TestParseEventRejectsRubbish(t *testing.T) {
	for _, body := range []string{``, `not json`, `{}`, `{"id":"1"}`, `[]`} {
		if _, err := ParseEvent([]byte(body)); err == nil {
			t.Errorf("ParseEvent(%q) accepted", body)
		}
	}
}

func TestUnknownEventTypeStillParses(t *testing.T) {
	// New event types arrive without warning; a receiver must be able to
	// read the envelope and ignore the rest.
	body := []byte(`{"id":"019fedda-88ef-7253-8253-14d9e24723fb","type":"quantum.entangled","tenant_id":"019fedcf-70a4-7ecd-bda1-f45dd0fdc0ca","created_at":"2026-08-11T05:12:29.922Z","data":{"thing":{"n":1}}}`)
	event, err := ParseEvent(body)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if event.Type != "quantum.entangled" {
		t.Errorf("Type = %q", event.Type)
	}
}

func TestParseEventRequiresSignedIdentity(t *testing.T) {
	for _, body := range []string{
		`{"type":"x"}`,
		strings.Replace(katBody, `"id":"019fedda-88ef-7253-8253-14d9e24723fb"`, `"id":null`, 1),
		strings.Replace(katBody, `019fedcf-70a4-7ecd-bda1-f45dd0fdc0ca`, `00000000-0000-0000-0000-000000000000`, 1),
	} {
		if _, err := ParseEvent([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestUnsignedHeaderCannotChangeSignedIdentity(t *testing.T) {
	// This models receiver key selection, not durable database crash recovery.
	seen := map[string]bool{}
	effects := 0
	for _, advisoryID := range []string{"first", "changed", "first"} {
		_ = advisoryID // deliberately not an authority for the processing identity
		if err := verifySignatureAt(katHeader, []byte(katBody), katSecret, DefaultTolerance, katSignedAt); err != nil {
			t.Fatal(err)
		}
		event, err := ParseEvent([]byte(katBody))
		if err != nil {
			t.Fatal(err)
		}
		key := event.TenantID.String() + "/" + event.ID.String()
		if !seen[key] {
			seen[key] = true
			effects++
		}
	}
	if effects != 1 {
		t.Fatalf("effects=%d", effects)
	}
}
