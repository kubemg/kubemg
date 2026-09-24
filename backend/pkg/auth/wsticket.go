package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/cache"
)

// wsTicketTTL is how long a minted ticket may be redeemed. A ticket only has
// to survive the moment between "the console asks for one" and "the browser's
// WebSocket constructor opens the handshake" — normally milliseconds. Seconds
// leaves headroom for a slow network without leaving a window worth replaying
// a leaked query string for.
const wsTicketTTL = 20 * time.Second

// wsTicketBytes is the entropy behind a ticket. It is opaque and single-use,
// so — unlike the JWT it stands in for — there is nothing in it to verify
// offline; its only job is to be unguessable and to be looked up once.
const wsTicketBytes = 32

// WSTicketStore holds minted tickets until they are redeemed or expire.
//
// It has to be shared by every replica. The console mints a ticket with one
// ordinary request and opens the WebSocket with the next, and a load balancer
// owes those two requests no affinity: a ticket held in the memory of the
// replica that minted it is refused by the replica that receives the upgrade.
// The production store is the database; the in-memory one NewManager starts
// with is correct only for a single process.
//
// It is an interface here so that pkg/auth keeps knowing nothing about
// storage — the same split as MachineTokenVerifier. Keys are the ticket's
// SHA-256, never the ticket, and the payload is opaque to the store.
type WSTicketStore interface {
	// PutWSTicket files a ticket's payload until expiresAt.
	PutWSTicket(ctx context.Context, hash string, payload []byte, expiresAt time.Time) error
	// TakeWSTicket removes a ticket and returns what it held, in one step: of
	// any number of concurrent callers presenting the same ticket, at most one
	// may see found. An expired ticket is not found.
	TakeWSTicket(ctx context.Context, hash string) (payload []byte, found bool, err error)
}

// UseWSTicketStore replaces the in-memory ticket store. Call it before the
// router serves anything.
func (m *Manager) UseWSTicketStore(store WSTicketStore) {
	if store != nil {
		m.wsTickets = store
	}
}

// IssueWSTicket mints a short-lived, single-use ticket bound to claims already
// verified by RequireAuth's header path, so it can carry no more privilege
// than the request that asked for it.
//
// This exists because a browser cannot set a header when it opens a
// WebSocket, so the interactive terminal and the browser shell put a
// credential on the query string instead — see QueryTokenParam. Putting the
// session JWT itself there means every proxy, load balancer and access log in
// front of the backend gets a copy of a credential good for the rest of the
// session. A ticket is worthless the moment it is used, or twenty seconds
// after it is minted, whichever comes first, so a copy sitting in a log line
// is not a session left to replay.
func (m *Manager) IssueWSTicket(ctx context.Context, claims *Claims) (string, error) {
	buf := make([]byte, wsTicketBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate websocket ticket: %w", err)
	}
	ticket := base64.RawURLEncoding.EncodeToString(buf)

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode websocket ticket: %w", err)
	}
	if err := m.wsTickets.PutWSTicket(ctx, hashWSTicket(ticket), payload, time.Now().Add(wsTicketTTL)); err != nil {
		return "", fmt.Errorf("store websocket ticket: %w", err)
	}
	return ticket, nil
}

// redeemWSTicket consumes a ticket, returning the claims it was minted for. A
// ticket answers at most once: whether this is its legitimate holder's only
// use or an attacker's replay of a leaked query string, the second
// presentation fails the same way the first success did not. A store that
// cannot be read refuses — an upgrade it cannot vouch for is not let through.
func (m *Manager) redeemWSTicket(ctx context.Context, ticket string) (*Claims, bool) {
	payload, found, err := m.wsTickets.TakeWSTicket(ctx, hashWSTicket(ticket))
	if err != nil || !found {
		return nil, false
	}
	claims := &Claims{}
	if err := json.Unmarshal(payload, claims); err != nil {
		return nil, false
	}
	return claims, true
}

func hashWSTicket(ticket string) string {
	sum := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(sum[:])
}

// memoryWSTickets is the single-process store NewManager starts with.
type memoryWSTickets struct {
	entries *cache.Cache[[]byte]
}

func newMemoryWSTickets() *memoryWSTickets {
	return &memoryWSTickets{entries: cache.New[[]byte](wsTicketTTL)}
}

func (s *memoryWSTickets) PutWSTicket(_ context.Context, hash string, payload []byte, expiresAt time.Time) error {
	s.entries.PutFor("", hash, payload, time.Until(expiresAt))
	return nil
}

func (s *memoryWSTickets) TakeWSTicket(_ context.Context, hash string) ([]byte, bool, error) {
	payload, ok := s.entries.Take(hash)
	return payload, ok, nil
}
