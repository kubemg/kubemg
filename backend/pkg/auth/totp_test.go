package auth

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B's SHA-1 seed, "12345678901234567890", in base32.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestTOTPMatchesTheRFCVectors(t *testing.T) {
	// The RFC prints eight digits; six are the low six of the same number.
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	} {
		got, err := TOTPCode(rfcSecret, TOTPStep(time.Unix(tc.unix, 0)))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("T=%d: got %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestVerifyTOTPAcceptsOneStepEitherSide(t *testing.T) {
	now := time.Unix(1234567890, 0)
	step := TOTPStep(now)
	for _, delta := range []int64{-1, 0, 1} {
		code, _ := TOTPCode(rfcSecret, step+delta)
		got, ok := VerifyTOTP(rfcSecret, code, 0, now)
		if !ok || got != step+delta {
			t.Errorf("delta %d: ok=%v step=%d", delta, ok, got)
		}
	}
	for _, delta := range []int64{-2, 2} {
		code, _ := TOTPCode(rfcSecret, step+delta)
		if _, ok := VerifyTOTP(rfcSecret, code, 0, now); ok {
			t.Errorf("delta %d accepted", delta)
		}
	}
}

// A code is good once: the step it matched is stored, and nothing at or before
// it is accepted again.
func TestVerifyTOTPRefusesAReplayedStep(t *testing.T) {
	now := time.Unix(1234567890, 0)
	step := TOTPStep(now)
	code, _ := TOTPCode(rfcSecret, step)
	if _, ok := VerifyTOTP(rfcSecret, code, step, now); ok {
		t.Fatal("a code for the last accepted step was accepted again")
	}
	if _, ok := VerifyTOTP(rfcSecret, code, step+1, now); ok {
		t.Fatal("a code older than the last accepted step was accepted")
	}
}

func TestVerifyTOTPToleratesWhatPeopleType(t *testing.T) {
	now := time.Unix(1234567890, 0)
	code, _ := TOTPCode(rfcSecret, TOTPStep(now))
	for _, typed := range []string{code[:3] + " " + code[3:], " " + code + " ", code[:3] + "-" + code[3:]} {
		if _, ok := VerifyTOTP(rfcSecret, typed, 0, now); !ok {
			t.Errorf("%q refused", typed)
		}
	}
	for _, typed := range []string{"", "12345", "1234567", "abcdef", "12345a"} {
		if _, ok := VerifyTOTP(rfcSecret, typed, 0, now); ok {
			t.Errorf("%q accepted", typed)
		}
	}
}

func TestVerifyTOTPRefusesAnUnreadableSecret(t *testing.T) {
	if _, ok := VerifyTOTP("not base32!", "123456", 0, time.Now()); ok {
		t.Fatal("an unreadable secret verified a code")
	}
}

func TestNewTOTPSecretIsBase32OfTwentyBytes(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	key, err := otpEncoding.DecodeString(secret)
	if err != nil || len(key) != totpSecretBytes {
		t.Fatalf("secret %q decodes to %d bytes (%v)", secret, len(key), err)
	}
	other, _ := NewTOTPSecret()
	if other == secret {
		t.Fatal("two secrets were equal")
	}
}

func TestTOTPURIIsWhatAnAuthenticatorScans(t *testing.T) {
	uri := TOTPURI("KubeMG", "alice@kubemg.example.com", rfcSecret)
	for _, want := range []string{
		"otpauth://totp/KubeMG:alice@kubemg.example.com?",
		"secret=" + rfcSecret, "issuer=KubeMG", "digits=6", "period=30", "algorithm=SHA1",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("%s missing %q", uri, want)
		}
	}
	png, err := TOTPQRCode(uri)
	if err != nil || !strings.HasPrefix(png, "data:image/png;base64,") {
		t.Fatalf("QR: %v %.40s", err, png)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes", len(codes))
	}
	seen := map[string]bool{}
	for _, code := range codes {
		if len(code) != 11 || code[5] != '-' {
			t.Errorf("%q is not xxxxx-xxxxx", code)
		}
		if strings.Trim(strings.Replace(code, "-", "", 1), recoveryAlphabet) != "" {
			t.Errorf("%q leaves the alphabet", code)
		}
		if LooksLikeTOTP(code) {
			t.Errorf("%q could be mistaken for a TOTP code", code)
		}
		if seen[code] {
			t.Errorf("%q repeated", code)
		}
		seen[code] = true
	}
	// Typed without the dash, in capitals, it is still the same code.
	code := codes[0]
	if HashRecoveryCode(code) != HashRecoveryCode(strings.ToUpper(strings.Replace(code, "-", "", 1))) {
		t.Fatal("the hash depends on how the code was typed")
	}
}
