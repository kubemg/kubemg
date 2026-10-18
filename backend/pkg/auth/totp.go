package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is HMAC-SHA1; every authenticator app speaks it.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"
)

// The TOTP parameters are RFC 6238's defaults and deliberately not settings:
// they are the only ones every authenticator app agrees on, and an issuer that
// asks for SHA-256 or eight digits is one some phones silently get wrong.
const (
	totpPeriod = 30
	totpDigits = 6
	// totpSkew is how many steps either side of now a code is accepted for —
	// one, so a phone a few seconds off still works and a code read out loud a
	// minute late does not.
	totpSkew = 1
	// totpSecretBytes is RFC 4226's recommended 160 bits.
	totpSecretBytes = 20
)

// ErrInvalidOTPSecret is returned for a stored secret that is not base32.
var ErrInvalidOTPSecret = errors.New("invalid one-time password secret")

var otpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret mints a TOTP shared secret, base32 as authenticator apps expect
// it typed or scanned.
func NewTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate one-time password secret: %w", err)
	}
	return otpEncoding.EncodeToString(buf), nil
}

// TOTPURI is the otpauth:// URI an authenticator app scans. The issuer is the
// label the app groups it under; the account says which install and which
// person, because one phone holds the production and the staging console.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(totpDigits))
	query.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + query.Encode()
}

// TOTPQRCode renders a URI as a PNG data: URI. It is drawn here rather than in
// the browser so the console gains no QR dependency, and so the secret is never
// handed to a third-party script to draw.
func TOTPQRCode(uri string) (string, error) {
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		return "", fmt.Errorf("render QR code: %w", err)
	}
	code.Scale = 6
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()), nil
}

// TOTPStep is the time step a moment falls in.
func TOTPStep(now time.Time) int64 { return now.Unix() / totpPeriod }

// TOTPCode is the code a secret produces for one time step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := otpEncoding.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return "", ErrInvalidOTPSecret
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000), nil
}

// VerifyTOTP checks a submitted code against a secret and returns the step it
// matched. A step at or before lastStep is refused even when the code is right:
// a code is good once, so one read over a shoulder or out of a phishing proxy's
// log cannot be replayed inside its thirty seconds. The caller must still
// advance the stored step atomically — two replicas can both get here.
func VerifyTOTP(secret, code string, lastStep int64, now time.Time) (int64, bool) {
	code = NormalizeOTP(code)
	if len(code) != totpDigits || strings.Trim(code, "0123456789") != "" {
		return 0, false
	}
	current := TOTPStep(now)
	for delta := int64(-totpSkew); delta <= totpSkew; delta++ {
		step := current + delta
		if step <= lastStep {
			continue
		}
		want, err := TOTPCode(secret, step)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// NormalizeOTP strips what people type between digits — a space from the app's
// "123 456" display, a dash from a recovery code — and folds case.
func NormalizeOTP(code string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '\t':
			return -1
		}
		return r
	}, strings.TrimSpace(code)))
}

// LooksLikeTOTP reports whether a normalised code has a TOTP code's shape, as
// opposed to a recovery code's. The two never overlap: a recovery code is ten
// characters.
func LooksLikeTOTP(code string) bool {
	code = NormalizeOTP(code)
	return len(code) == totpDigits && strings.Trim(code, "0123456789") == ""
}

// RecoveryCodeCount is how many recovery codes a set holds.
const RecoveryCodeCount = 10

// recoveryAlphabet leaves out the characters people misread off paper.
const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// NewRecoveryCodes mints a set of single-use recovery codes, formatted
// `xxxxx-xxxxx`. Ten characters of a 31-letter alphabet is ~50 bits each: far
// past guessing behind the sign-in throttle, so a plain SHA-256 is enough to
// store them — a slow hash would protect a secret that is not guessable anyway.
func NewRecoveryCodes() ([]string, error) {
	codes := make([]string, RecoveryCodeCount)
	for i := range codes {
		var b bytes.Buffer
		for b.Len() < 11 {
			if b.Len() == 5 {
				b.WriteByte('-')
			}
			c, err := recoveryChar()
			if err != nil {
				return nil, err
			}
			b.WriteByte(c)
		}
		codes[i] = b.String()
	}
	return codes, nil
}

// recoveryChar draws one character uniformly: a byte past the largest multiple
// of the alphabet's length is drawn again rather than folded, which would
// favour the first few letters.
func recoveryChar() (byte, error) {
	limit := 256 - 256%len(recoveryAlphabet)
	var one [1]byte
	for {
		if _, err := rand.Read(one[:]); err != nil {
			return 0, fmt.Errorf("generate recovery codes: %w", err)
		}
		if int(one[0]) < limit {
			return recoveryAlphabet[int(one[0])%len(recoveryAlphabet)], nil
		}
	}
}

// HashRecoveryCode is the key a recovery code is stored and redeemed under.
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeOTP(code)))
	return hex.EncodeToString(sum[:])
}
