package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * How long a password may live.
 *
 * One policy for every account whose password is held here — local people,
 * never a federated account (its password, an LDAP bind's included, is the
 * directory's) or a machine (it has none): `password_max_age_days`, 0 = off.
 * An administrator can also expire one account's password by hand, which is a
 * flag rather than a backdated clock so the policy's own window is untouched.
 *
 * Expiry is enforced on the **session**, not at sign-in alone. A password that
 * is due still signs in — it was the right password — and the session it opens
 * reaches only the routes that let its owner change it; every other route
 * answers 403 with `code: password_change_required`, which the console turns
 * into its change-password screen. An open session whose password falls due,
 * or is expired by an administrator, is held the same way at its next call.
 * Proxy-scoped credentials (kubeconfigs, CLI sessions) and machine tokens are
 * deliberately not gated: they are revoked on their own terms.
 *
 * The gate reads a published snapshot — account id → due instant — lock-free on
 * every authenticated call, the credential register's shape: republished after
 * every write that moves a due date in this process, refreshed on a timer for
 * the others, and a failed refresh keeps the previous snapshot.
 */

const (
	// verbPasswordExpire is an administrator forcing somebody's password to be
	// changed. Like password-change it names no cluster.
	verbPasswordExpire = "password-expire"
	// passwordChangeRequiredCode is the refusal's stable answer, read by the
	// console rather than the sentence beside it.
	passwordChangeRequiredCode = "password_change_required"
	// passwordGateRefreshInterval is how soon a sibling replica's write, or a
	// clock passing a due date, reaches this one's gate.
	passwordGateRefreshInterval = 30 * time.Second
)

// Policy bounds, in days: the audit window's, for the same reason — past ten
// years a rotation policy is not one.
const (
	minPasswordMaxAgeDays = 1
	maxPasswordMaxAgeDays = 3650
)

// passwordGateExempt are the routes a session whose password is due still
// reaches: reading who it is, and changing the password.
var passwordGateExempt = map[string]bool{
	http.MethodGet + " /api/v1/auth/me":        true,
	http.MethodPost + " /api/v1/auth/password": true,
}

// passwordDue is when an account's password stops being accepted, and false
// when nothing makes it due. An administrator's expiry and the policy's window
// both count; the earlier wins.
func passwordDue(account db.PasswordAccount, maxAgeDays int) (time.Time, bool) {
	var due time.Time
	found := false
	if maxAgeDays > 0 {
		changed := account.CreatedAt
		if account.PasswordChangedAt != nil {
			changed = *account.PasswordChangedAt
		}
		due, found = changed.Add(time.Duration(maxAgeDays)*24*time.Hour), true
	}
	if at := account.PasswordExpiredAt; at != nil && (!found || at.Before(due)) {
		due, found = *at, true
	}
	return due, found
}

// passwordGate is the published snapshot. The zero value refuses nothing.
type passwordGate struct {
	due atomic.Pointer[map[uint]time.Time]
}

func (g *passwordGate) store(due map[uint]time.Time) { g.due.Store(&due) }

// dueAt reports when id's password falls due, if anything makes it.
func (g *passwordGate) dueAt(id uint) (time.Time, bool) {
	if g == nil {
		return time.Time{}, false
	}
	snapshot := g.due.Load()
	if snapshot == nil {
		return time.Time{}, false
	}
	due, ok := (*snapshot)[id]
	return due, ok
}

// publishPasswordGate resolves the snapshot. A read that fails keeps the
// previous one: an unreadable table means no *new* due date is known, never
// that every session is held — a database blip must not lock the console.
func (s *server) publishPasswordGate(ctx context.Context) {
	if s.passwords == nil {
		return
	}
	accounts, err := s.store.PasswordAccounts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("could not refresh password expiry; keeping the previous set",
				slog.String("error", err.Error()))
		}
		return
	}
	days := s.settings(ctx).PasswordMaxAgeDays
	due := make(map[uint]time.Time, len(accounts))
	for _, account := range accounts {
		if at, ok := passwordDue(account, days); ok {
			due[account.ID] = at
		}
	}
	s.passwords.store(due)
}

// startPasswordGateRefresher publishes the snapshot at once and then on a
// timer, for as long as ctx lives.
func (s *server) startPasswordGateRefresher(ctx context.Context) {
	ticker := time.NewTicker(passwordGateRefreshInterval)
	defer ticker.Stop()
	for {
		s.publishPasswordGate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// passwordExpired reports whether id's password is due now.
func (s *server) passwordExpired(id uint, now time.Time) bool {
	due, ok := s.passwords.dueAt(id)
	return ok && !now.Before(due)
}

// gatePasswordAge is the auth.SessionGate: a session whose password is due
// reaches only passwordGateExempt.
func (s *server) gatePasswordAge(c *gin.Context, claims *auth.Claims) bool {
	if !s.passwordExpired(claims.UserID, time.Now()) {
		return true
	}
	if passwordGateExempt[c.Request.Method+" "+c.FullPath()] {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error": "your password has expired — change it to continue",
		"code":  passwordChangeRequiredCode,
	})
	return false
}

// annotatePassword adds what the policy says about an account to its
// rendering: when its password falls due, and whether it already has.
func (s *server) annotatePassword(out *userResponse) {
	due, ok := s.passwords.dueAt(out.ID)
	if !ok {
		return
	}
	out.PasswordExpiresAt = &due
	out.PasswordChangeRequired = !time.Now().Before(due)
}

// expireUserPassword is an administrator's "change it at next sign-in" — and,
// for a session already open, at its next call. Never on your own account (you
// change yours instead), and only a super admin expires a super admin
// (loadManageableUser). An account whose password is not held here is refused
// by name rather than flagged to no effect.
func (s *server) expireUserPassword(c *gin.Context) {
	caller, target, ok := s.loadManageableUser(c)
	if !ok {
		return
	}
	if target.ID == caller.ID {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "you cannot expire your own password — change it from your profile instead",
		})
		return
	}
	// A machine account never gets here: loadManageableUser answers 404 for one.
	if target.IsFederated() {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this account signs in through an identity provider, so its password is not held here",
		})
		return
	}

	ctx := c.Request.Context()
	changed, err := s.store.ExpirePassword(ctx, target.ID, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not expire the password"})
		return
	}
	if changed {
		s.recordPasswordExpiry(c, caller, target)
	}
	s.publishPasswordGate(ctx)
	if changed {
		s.kickPasswordNotices()
	}

	updated, err := s.store.UserByID(ctx, target.ID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the user"})
		return
	}
	out := toUserResponse(updated)
	s.annotatePassword(&out)
	c.JSON(http.StatusOK, out)
}

// recordPasswordExpiry puts the act in the trail with the identities crossed:
// the administrator as the user, the account as the impersonated identity.
func (s *server) recordPasswordExpiry(c *gin.Context, actor, subject *db.User) {
	if s.auditor == nil {
		return
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:               time.Now().UTC(),
		UserID:           actor.ID,
		Username:         actor.Username,
		Verb:             verbPasswordExpire,
		Method:           c.Request.Method,
		Path:             c.Request.URL.Path,
		Resource:         "users",
		Status:           http.StatusOK,
		ImpersonatedUser: subject.Username,
	})
}

// passwordPolicyWarnings discloses a policy that will expire passwords nobody
// is warned about.
func (s *server) passwordPolicyWarnings(ctx context.Context, effective runtimeSettings) []string {
	if effective.PasswordMaxAgeDays == 0 {
		return nil
	}
	relay, err := s.loadSMTP(ctx)
	if err != nil || relay != nil {
		return nil
	}
	return []string{fmt.Sprintf(
		"Passwords expire after %d days, but no mail relay is configured, so nobody is reminded before "+
			"theirs does. Configure one under Email.", effective.PasswordMaxAgeDays)}
}
