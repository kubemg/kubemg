package jit

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

/*
 * Deciding from outside the console.
 *
 * An approval that can only be given in KubeMG is an approval that waits until
 * somebody opens KubeMG, which on a Friday evening is the difference between a
 * bounded elevation and a standing one somebody granted in advance "to be safe".
 * So a notification carries two ways to act: a link into the console, which is
 * the path that always works, and a signed token that lets an interactive chat
 * button decide without one.
 *
 * The token is deliberately **not sufficient on its own**. It authorises one
 * action on one request and expires, but it names no approver — a chat webhook
 * has no KubeMG identity, and minting one would mean the audit record for a
 * production elevation said "slack" where the approver's name belongs. So the
 * callback needs both: this token, *and* a caller who resolves to an active KubeMG
 * administrator who is not the requester. What the token buys is that possession
 * of a username is not enough; what the identity buys is that the record is
 * honest.
 *
 * The signature is HMAC-SHA256 over the whole payload with the server's signing
 * secret. There is no revocation list and none is needed: applying the action
 * moves the request off `pending`, so a replayed token finds nothing to decide.
 */

// Actions a callback may carry. They are the two decisions a pending request
// admits; a revocation is deliberately not one of them, because revoking is not
// time-critical and the console is where somebody can see what they are ending.
const (
	ActionApprove = "approve"
	ActionReject  = "reject"
)

// callbackPath is where a signed decision is posted. It is part of this package
// because the notification has to render the absolute URL and the router has to
// serve it, and having the two agree by construction beats having them agree by
// convention.
const callbackPath = "/api/v1/jit/webhooks/callback"

// CallbackPath is that path, for the router.
func CallbackPath() string { return callbackPath }

// callbackTTL is how long a button in a message keeps working. Two days, because
// a request made on a Friday is still worth approving on a Monday morning — and
// bounded, because a message thread is a durable place and a token that never
// expired would be a standing approval capability sitting in chat history.
const callbackTTL = 48 * time.Hour

// Action is what a signed callback token authorises.
type Action struct {
	RequestID string
	Action    string
	Expires   time.Time
}

// SignAction renders a token authorising one action on one request.
//
// The payload is readable — it is not a secret, it is a claim — and the signature
// is what makes it unforgeable. Rendering it as text rather than as JSON keeps the
// signed bytes canonical: two encoders disagreeing about field order would make a
// valid token fail to verify.
func SignAction(secret []byte, action Action) string {
	payload := actionPayload(action)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func actionPayload(action Action) string {
	return strings.Join([]string{
		"v1",
		action.RequestID,
		action.Action,
		strconv.FormatInt(action.Expires.UTC().Unix(), 10),
	}, "|")
}

// Callback token errors, distinguished because they call for different answers: a
// malformed or wrongly-signed token is somebody probing, while an expired one is
// an approver clicking an old message and deserves to be told so.
var (
	ErrBadToken     = errors.New("approval token is not valid")
	ErrTokenExpired = errors.New("approval token has expired")
)

// ParseAction verifies a token and returns what it authorises.
func ParseAction(secret []byte, raw string, now time.Time) (Action, error) {
	if len(secret) == 0 {
		// A server with no signing secret cannot have minted this, so nothing it
		// receives is verifiable. Refusing is the only safe answer.
		return Action{}, ErrBadToken
	}

	encoded, signature, ok := strings.Cut(strings.TrimSpace(raw), ".")
	if !ok {
		return Action{}, ErrBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Action{}, ErrBadToken
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return Action{}, ErrBadToken
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	// Constant time, so a wrong signature does not leak how much of it was right.
	if subtle.ConstantTimeCompare(provided, mac.Sum(nil)) != 1 {
		return Action{}, ErrBadToken
	}

	parts := strings.Split(string(payload), "|")
	if len(parts) != 4 || parts[0] != "v1" {
		return Action{}, ErrBadToken
	}
	unix, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return Action{}, ErrBadToken
	}
	action := Action{
		RequestID: parts[1],
		Action:    parts[2],
		Expires:   time.Unix(unix, 0).UTC(),
	}
	if action.RequestID == "" ||
		(action.Action != ActionApprove && action.Action != ActionReject) {
		return Action{}, ErrBadToken
	}
	if !action.Expires.After(now) {
		return Action{}, ErrTokenExpired
	}
	return action, nil
}

// newRequestID mints the identifier a request is known by everywhere: in the
// database, in a chat message, in a signed token and in the audit trail.
//
// It is a random UUIDv4 rather than a sequence precisely because of those last
// two. A sequential id in a Slack message tells a reader what the next request's
// id will be, and an approval flow whose identifiers can be guessed is one where
// probing is a strategy.
func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail on any supported platform, and a request id
		// that is not random is not one this workflow should mint — so this is the
		// one place here that panics rather than degrading.
		panic(fmt.Sprintf("generate request id: %v", err))
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
