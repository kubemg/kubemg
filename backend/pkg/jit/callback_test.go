package jit

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The token is what stands between a chat message and a production elevation, so
// the cases worth writing are the ones where it must fail.

func TestSignAndParseAction(t *testing.T) {
	secret := []byte("signing-secret")
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	original := Action{
		RequestID: "8f14e45f-ea1b-4a1a-9f4d-2b0a1c3d4e5f",
		Action:    ActionApprove,
		Expires:   now.Add(time.Hour),
	}

	parsed, err := ParseAction(secret, SignAction(secret, original), now)
	if err != nil {
		t.Fatalf("parse a token we just signed: %v", err)
	}
	if parsed.RequestID != original.RequestID || parsed.Action != original.Action {
		t.Fatalf("round trip lost the claim: %+v", parsed)
	}
	if !parsed.Expires.Equal(original.Expires) {
		t.Fatalf("want the expiry preserved, got %v", parsed.Expires)
	}
}

func TestParseActionRefusals(t *testing.T) {
	secret := []byte("signing-secret")
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	valid := SignAction(secret, Action{
		RequestID: "req-1", Action: ActionApprove, Expires: now.Add(time.Hour),
	})

	cases := []struct {
		name   string
		secret []byte
		token  string
		at     time.Time
		want   error
	}{
		{
			name:   "another server's secret",
			secret: []byte("different-secret"),
			token:  valid,
			at:     now,
			want:   ErrBadToken,
		},
		{
			// The whole point of signing the payload: editing the claim invalidates it.
			name:   "a payload edited in place",
			secret: secret,
			token:  tamper(valid),
			at:     now,
			want:   ErrBadToken,
		},
		{
			name:   "no signature at all",
			secret: secret,
			token:  strings.Split(valid, ".")[0],
			at:     now,
			want:   ErrBadToken,
		},
		{
			// A message thread is a durable place, so a button in one has to stop
			// working eventually.
			name:   "past its expiry",
			secret: secret,
			token:  valid,
			at:     now.Add(2 * time.Hour),
			want:   ErrTokenExpired,
		},
		{
			// A server with no secret cannot have minted anything, so it must trust
			// nothing rather than verify against an empty key.
			name:   "a server with no signing secret",
			secret: nil,
			token:  valid,
			at:     now,
			want:   ErrBadToken,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAction(tc.secret, tc.token, tc.at); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// tamper flips a character inside the encoded payload, leaving the signature as it
// was — the shape an attempt to change the request id or the action would take.
func tamper(token string) string {
	payload, signature, _ := strings.Cut(token, ".")
	edited := []byte(payload)
	if edited[3] == 'A' {
		edited[3] = 'B'
	} else {
		edited[3] = 'A'
	}
	return string(edited) + "." + signature
}

func TestNewRequestIDIsUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := newRequestID()
		if len(id) != 36 {
			t.Fatalf("want a 36-character UUID, got %q", id)
		}
		if seen[id] {
			t.Fatalf("newRequestID repeated %q", id)
		}
		seen[id] = true
	}
}
