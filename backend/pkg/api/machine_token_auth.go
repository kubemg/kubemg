package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// touchInterval bounds how often a token's last-used stamp is written. This read
// sits in front of every proxied call a pipeline makes, and a build that lists
// pods in a loop would otherwise turn one indexed select into a write per call.
// Knowing a credential was used in the last few minutes answers the question
// this column exists for — which of these is abandoned — as well as knowing the
// second would.
const touchInterval = 5 * time.Minute

// touchTimeout bounds the background write. It uses a context of its own because
// the request that triggered it is long gone by the time this matters, and an
// observation must never outlive or delay what it observed.
const touchTimeout = 5 * time.Second

// machineTokenStore is the slice of persistence the verifier needs. It is
// separate from Store so the authentication path can be tested as the small
// thing it is.
type machineTokenStore interface {
	MachineTokenByHash(ctx context.Context, hash string) (*db.MachineToken, error)
	TouchMachineToken(ctx context.Context, id uint, at time.Time) error
	UserByID(ctx context.Context, id uint) (*db.User, error)
}

// machineTokenVerifier turns a stored credential into claims.
//
// What it deliberately does not do is decide anything about access. It answers
// "is this credential live, and whose is it" and hands back the same claims a
// session would have carried, so the grant, the namespace scope, the cluster's
// own RBAC and the audit record are all resolved afterwards by the code that
// already resolves them. Revoking a grant stops a pipeline at its next call
// without this file knowing that grants exist.
type machineTokenVerifier struct {
	store machineTokenStore
	now   func() time.Time

	mu      sync.Mutex
	touched map[uint]time.Time
}

func newMachineTokenVerifier(store machineTokenStore) *machineTokenVerifier {
	return &machineTokenVerifier{store: store, touched: map[uint]time.Time{}}
}

// VerifyMachineToken implements auth.MachineTokenVerifier.
func (v *machineTokenVerifier) VerifyMachineToken(ctx context.Context, secret string) (*auth.Claims, error) {
	token, err := v.store.MachineTokenByHash(ctx, auth.HashMachineToken(secret))
	if err != nil {
		// A hash that matches nothing and a database that would not answer are
		// the same to a caller: this credential does not work right now. The
		// distinction belongs in the server's own logs, not in a reply to
		// somebody presenting an unknown secret.
		return nil, auth.ErrInvalidToken
	}
	if !auth.EqualMachineTokenHash(token.TokenHash, auth.HashMachineToken(secret)) {
		return nil, auth.ErrInvalidToken
	}
	now := v.clock()
	if !token.Usable(now) {
		return nil, auth.ErrInvalidToken
	}

	user, err := v.store.UserByID(ctx, token.UserID)
	if err != nil || user == nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, auth.ErrInvalidToken
		}
		return nil, auth.ErrInvalidToken
	}
	// Disabling the account has to stop its credentials at once, which is the
	// same rule a session follows in currentUser.
	if !user.IsActive {
		return nil, auth.ErrInvalidToken
	}
	// A token is only ever issued to a machine account, but the check is here
	// rather than assumed: this is the one path where a row deciding what an
	// account is would otherwise go unread.
	if !user.IsMachine() {
		return nil, auth.ErrInvalidToken
	}
	user.Normalize()

	v.touch(token.ID, now)

	return &auth.Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		// The same scope a generated kubeconfig carries, so the same middleware
		// rule confines this to one cluster's proxy route and nothing else. A
		// credential in a CI secret store must not also open the console's API.
		Scope:     auth.ScopeProxy,
		ClusterID: token.ClusterID,
	}, nil
}

// touch records the use, at most once per token per touchInterval, off the
// request's own path.
func (v *machineTokenVerifier) touch(id uint, now time.Time) {
	v.mu.Lock()
	last, seen := v.touched[id]
	if seen && now.Sub(last) < touchInterval {
		v.mu.Unlock()
		return
	}
	v.touched[id] = now
	v.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), touchTimeout)
		defer cancel()
		// A failure here is not worth reporting anywhere a caller can see: the
		// call it belongs to has already been authenticated, and the next use
		// will try again.
		_ = v.store.TouchMachineToken(ctx, id, now)
	}()
}

func (v *machineTokenVerifier) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now().UTC()
}
