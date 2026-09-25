package secretbox

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func testBox(t *testing.T, fill byte) *Box {
	t.Helper()
	box, err := New(bytes.Repeat([]byte{fill}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSealOpenRoundTrip(t *testing.T) {
	box := testBox(t, 1)
	for _, plain := range []string{"kmg_abc", "a much longer secret with unicode ✓ and\nnewlines"} {
		sealed, err := box.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(sealed, Prefix) || strings.Contains(sealed, plain) {
			t.Fatalf("sealed value %q does not look encrypted", sealed)
		}
		again, _ := box.Seal(plain)
		if again == sealed {
			t.Fatal("two seals of one value must differ (random nonce)")
		}
		opened, err := box.Open(sealed)
		if err != nil || opened != plain {
			t.Fatalf("Open = %q, %v; want %q", opened, err, plain)
		}
	}
}

func TestEmptyStaysEmpty(t *testing.T) {
	box := testBox(t, 1)
	if sealed, err := box.Seal(""); err != nil || sealed != "" {
		t.Fatalf("Seal(\"\") = %q, %v", sealed, err)
	}
}

func TestTamperedCiphertextRefused(t *testing.T) {
	box := testBox(t, 1)
	sealed, _ := box.Seal("secret")
	raw, _ := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, Prefix))
	raw[len(raw)-1] ^= 0x01
	tampered := Prefix + base64.RawStdEncoding.EncodeToString(raw)
	if _, err := box.Open(tampered); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("tampered: err = %v, want ErrKeyMismatch", err)
	}
	for _, junk := range []string{Prefix, Prefix + "!!!", Prefix + "AAAA"} {
		if _, err := box.Open(junk); !errors.Is(err, ErrKeyMismatch) {
			t.Fatalf("Open(%q): err = %v, want ErrKeyMismatch", junk, err)
		}
	}
}

func TestWrongKeyRefused(t *testing.T) {
	sealed, _ := testBox(t, 1).Seal("secret")
	if _, err := testBox(t, 2).Open(sealed); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}
}

func TestNoKeyPassesPlaintextAndRefusesCiphertext(t *testing.T) {
	var none *Box
	if none.Enabled() {
		t.Fatal("nil box reports enabled")
	}
	if sealed, err := none.Seal("plain"); err != nil || sealed != "plain" {
		t.Fatalf("no-key Seal = %q, %v", sealed, err)
	}
	if opened, err := none.Open("plain"); err != nil || opened != "plain" {
		t.Fatalf("no-key Open = %q, %v", opened, err)
	}
	sealed, _ := testBox(t, 1).Seal("secret")
	if _, err := none.Open(sealed); !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("no-key Open(ciphertext): err = %v, want ErrKeyRequired", err)
	}
	// And a keyed box reads plaintext written before the key was set.
	if opened, err := testBox(t, 1).Open("legacy"); err != nil || opened != "legacy" {
		t.Fatalf("keyed Open(plaintext) = %q, %v", opened, err)
	}
}

func TestParseKey(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	for _, raw := range []string{
		hex.EncodeToString(key),
		base64.StdEncoding.EncodeToString(key),
		base64.RawURLEncoding.EncodeToString(key),
		"  " + base64.StdEncoding.EncodeToString(key) + "\n",
	} {
		box, err := Parse(raw)
		if err != nil || !box.Enabled() {
			t.Fatalf("Parse(%q) = %v, %v", raw, box, err)
		}
	}
	if box, err := Parse(""); err != nil || box != nil {
		t.Fatalf("Parse(\"\") = %v, %v; want no key", box, err)
	}
	for _, bad := range []string{
		"correct horse battery staple",
		hex.EncodeToString(key[:16]),
		base64.StdEncoding.EncodeToString(append(key, 0)),
	} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) accepted a wrong-length key", bad)
		}
	}
	if _, err := New(key[:31]); err == nil {
		t.Fatal("New accepted a 31-byte key")
	}
}
