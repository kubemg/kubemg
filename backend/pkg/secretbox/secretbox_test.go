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

func rotatingBox(t *testing.T, current, previous byte) *Box {
	t.Helper()
	box, err := NewRotating(bytes.Repeat([]byte{current}, KeySize), bytes.Repeat([]byte{previous}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestRotatingBoxOpensBothAndSealsUnderCurrent(t *testing.T) {
	underOld, _ := testBox(t, 1).Seal("old-secret")
	box := rotatingBox(t, 2, 1)
	if !box.Rotating() {
		t.Fatal("a box with a previous key must report rotating")
	}

	plain, previous, err := box.OpenRotated(underOld)
	if err != nil || plain != "old-secret" || !previous {
		t.Fatalf("OpenRotated(old) = %q, %v, %v; want the value, under the previous key", plain, previous, err)
	}

	resealed, _ := box.Seal(plain)
	if _, err := testBox(t, 1).Open(resealed); !errors.Is(err, ErrKeyMismatch) {
		t.Fatal("a rotating box must seal under the current key, never the previous one")
	}
	if opened, err := testBox(t, 2).Open(resealed); err != nil || opened != "old-secret" {
		t.Fatalf("current key Open(resealed) = %q, %v", opened, err)
	}
	if _, previous, err := box.OpenRotated(resealed); err != nil || previous {
		t.Fatalf("a value under the current key reported previous=%v, err=%v", previous, err)
	}
	if _, previous, _ := box.OpenRotated("legacy"); previous {
		t.Fatal("plaintext is never under the previous key")
	}
}

func TestRotatingBoxStillRefusesAThirdKey(t *testing.T) {
	sealed, _ := testBox(t, 3).Seal("secret")
	if _, _, err := rotatingBox(t, 2, 1).OpenRotated(sealed); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}
}

func TestRotationRefusals(t *testing.T) {
	key := bytes.Repeat([]byte{1}, KeySize)
	if _, err := NewRotating(nil, key); !errors.Is(err, ErrPreviousWithoutKey) {
		t.Fatalf("previous without a key: err = %v, want ErrPreviousWithoutKey", err)
	}
	if _, err := NewRotating(key, key); !errors.Is(err, ErrPreviousIsCurrent) {
		t.Fatalf("same key twice: err = %v, want ErrPreviousIsCurrent", err)
	}
	if _, err := NewRotating(key, key[:16]); err == nil {
		t.Fatal("a wrong-length previous key was accepted")
	}
	if _, err := ParseRotating(base64.StdEncoding.EncodeToString(key), "not a key"); err == nil {
		t.Fatal("ParseRotating accepted a malformed previous key")
	}
	box, err := ParseRotating(base64.StdEncoding.EncodeToString(key), "")
	if err != nil || box.Rotating() {
		t.Fatalf("no previous key: box rotating=%v, err=%v", box.Rotating(), err)
	}
}
